package telegramoperator

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
)

const (
	DefaultDailyExplanationRequests uint32 = 20
	MaxDailyExplanationRequests     uint32 = 1000

	explanationBudgetVersion  = 1
	maxExplanationBudgetBytes = 256
)

var errExplanationBudgetExhausted = errors.New("daily explanation request budget is exhausted")

// ExplanationBudget durably reserves capacity before an explanation provider
// is called. A reservation is intentionally not refunded after provider errors.
type ExplanationBudget interface {
	Reserve(time.Time) error
}

// FileExplanationBudget stores a bounded UTC-day request count in a private,
// atomically replaced file.
type FileExplanationBudget struct {
	path       string
	dailyLimit uint32
	mu         sync.Mutex
}

type explanationBudgetDocument struct {
	Version  uint32 `json:"version"`
	DayUTC   string `json:"day_utc"`
	Requests uint32 `json:"requests"`
}

func NewFileExplanationBudget(path string, dailyLimit uint32) (*FileExplanationBudget, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("explanation budget path must be a clean absolute path")
	}
	if dailyLimit == 0 || dailyLimit > MaxDailyExplanationRequests {
		return nil, errors.New("daily explanation request limit is invalid")
	}
	return &FileExplanationBudget{path: path, dailyLimit: dailyLimit}, nil
}

func (b *FileExplanationBudget) Reserve(now time.Time) error {
	if b == nil || b.path == "" || b.dailyLimit == 0 ||
		b.dailyLimit > MaxDailyExplanationRequests || now.IsZero() {
		return errors.New("explanation budget is unavailable")
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	lock, err := acquirePrivateFileLock(b.path + ".lock")
	if err != nil {
		return errors.New("explanation budget is unavailable")
	}
	defer lock.Close()

	day := now.UTC().Format(time.DateOnly)
	document, err := readExplanationBudget(b.path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		document = explanationBudgetDocument{Version: explanationBudgetVersion, DayUTC: day}
	case err != nil:
		return errors.New("explanation budget is unavailable")
	case document.DayUTC > day:
		return errors.New("explanation budget is unavailable")
	case document.DayUTC < day:
		document.DayUTC = day
		document.Requests = 0
	case document.Requests >= b.dailyLimit:
		return errExplanationBudgetExhausted
	}
	document.Requests++
	encoded, err := json.Marshal(document)
	if err != nil {
		return errors.New("encode explanation budget")
	}
	if err := securefile.ReplacePrivate(
		b.path, append(encoded, '\n'), maxExplanationBudgetBytes,
	); err != nil {
		return errors.New("write explanation budget")
	}
	return nil
}

func readExplanationBudget(path string) (explanationBudgetDocument, error) {
	data, err := securefile.ReadPrivate(path, maxExplanationBudgetBytes)
	if err != nil {
		return explanationBudgetDocument{}, err
	}
	var document explanationBudgetDocument
	if err := strictjson.Decode(data, &document); err != nil {
		return explanationBudgetDocument{}, errors.New("decode explanation budget")
	}
	day, err := time.Parse(time.DateOnly, document.DayUTC)
	if err != nil || day.Format(time.DateOnly) != document.DayUTC ||
		document.Version != explanationBudgetVersion || document.Requests == 0 ||
		document.Requests > MaxDailyExplanationRequests {
		return explanationBudgetDocument{}, errors.New("explanation budget is invalid")
	}
	return document, nil
}

var _ ExplanationBudget = (*FileExplanationBudget)(nil)
