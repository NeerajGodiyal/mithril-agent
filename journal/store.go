package journal

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/fileowner"
	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
)

const (
	maxJournalBytes = 64 << 20
	maxRecordBytes  = 1 << 20
	maxRecords      = 65_536

	// Format identifies the journal encoding and hash domain.
	Format     = "mithril-agent/journal-v1"
	hashDomain = Format

	// EventRotated is the first record of every new active segment. It carries
	// the sealed segment's identity and chain head, and because it is written
	// into the staged file BEFORE that file becomes the active journal, a new
	// active file is never durably empty — which is what makes deletion of the
	// newest sealed segment detectable rather than silent.
	EventRotated = "journal.rotated"

	// maxSegments bounds how many sealed segments one journal may accumulate.
	// It is a backstop against unbounded directory and memory growth, not an
	// operational limit: at the rotation threshold it is decades of running.
	maxSegments = 1024

	segmentSuffix = ".seg-"
	stagedSuffix  = ".next"
	lockSuffix    = ".lock"
)

// rotationMarker is the payload of an EventRotated record.
type rotationMarker struct {
	SealedSegment      int    `json:"sealed_segment"`
	SealedLastSequence uint64 `json:"sealed_last_sequence"`
	SealedChainHead    string `json:"sealed_chain_head"`
}

// scanSeed continues a hash chain across a segment boundary. The zero value
// starts a fresh chain, which is exactly what a single-file journal needs, so
// the non-rotating path is unchanged.
type scanSeed struct {
	startSequence uint64
	prevHash      string
	lastAt        time.Time
}

// ErrLocked reports that another process owns the journal writer lock.
var ErrLocked = errors.New("journal is already open by another process")

type Record struct {
	Sequence uint64          `json:"sequence"`
	At       time.Time       `json:"at"`
	Type     string          `json:"type"`
	ActionID string          `json:"action_id,omitempty"`
	Payload  json.RawMessage `json:"payload"`
	PrevHash string          `json:"prev_hash"`
	Hash     string          `json:"hash"`
}

type unsignedRecord struct {
	Domain   string          `json:"domain"`
	Sequence uint64          `json:"sequence"`
	At       time.Time       `json:"at"`
	Type     string          `json:"type"`
	ActionID string          `json:"action_id,omitempty"`
	Payload  json.RawMessage `json:"payload"`
	PrevHash string          `json:"prev_hash"`
}

type Store struct {
	mu           sync.Mutex
	file         *os.File
	reserve      *os.File
	reservePath  string
	reserveBytes int64
	records      []Record
	poison       error

	// rotating stores seal the active file into a numbered segment when it
	// approaches the per-file caps, so a runner can append indefinitely.
	// Rotation is opt-in because the signer's authorization ledger reuses this
	// type and requires its own header at records[0].
	rotating bool
	basePath string
	// lock is the stable lock file. Exclusivity has to outlive the active
	// file's identity, which rotation changes, so it cannot live on the data
	// file alone.
	lock *os.File
	// activeStart is the index in records where the active segment begins.
	// The per-file caps apply from here; sequence and hash chain stay global.
	activeStart int
	segments    int
}

type Stats struct {
	// Records is the whole history across every segment. ActiveRecords is the
	// count in the segment currently being appended to, which is what the
	// per-file caps below actually bound — dividing Records by MaxRecords
	// once rotation exists compares a growing total against a per-segment
	// limit and climbs forever.
	ActiveRecords      int
	Records            int
	Bytes              int64
	ReservedBytes      int64
	MaxRecords         int
	MaxBytes           int64
	SendStartedRecords int
	SubmittedRecords   int
}

// Verification summarizes a read-only validation of the complete journal.
type Verification struct {
	Records            int
	Bytes              int64
	FileSHA256         string
	ChainHeadSHA256    string
	SendStartedRecords int
	SubmittedRecords   int
}

func Open(path string) (*Store, error) {
	return open(path, false)
}

func open(path string, rotating bool) (*Store, error) {
	if path == "" {
		return nil, errors.New("journal path is empty")
	}
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return nil, fmt.Errorf("create journal directory: %w", err)
	}
	if err := validateJournalDirectory(parent); err != nil {
		return nil, err
	}
	if !rotating {
		// A non-rotating open of a rotated journal would read only the active
		// segment and silently lose the history every latch is derived from.
		// The scanner would reject it anyway on sequence contiguity; saying so
		// plainly is the difference between a diagnosable error and a puzzle.
		segments, staged, err := hasSegments(path)
		if err != nil {
			return nil, err
		}
		if segments {
			return nil, errors.New("journal has rotated segments; reopen with rotation enabled")
		}
		if staged {
			return nil, errors.New("journal has an incomplete rotation; reopen with rotation enabled")
		}
	}
	if rotating {
		return openRotating(path, parent)
	}
	_, statErr := os.Lstat(path)
	created := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !created {
		return nil, fmt.Errorf("inspect journal: %w", statErr)
	}
	if err := rejectSymlink(path); err != nil {
		return nil, err
	}
	file, err := openFile(path)
	if err != nil {
		return nil, fmt.Errorf("open journal: %w", err)
	}
	closeOnError := func(err error) (*Store, error) {
		_ = file.Close()
		return nil, err
	}
	if err := lockFile(file); err != nil {
		return closeOnError(fmt.Errorf("lock journal: %w", err))
	}
	if created {
		if err := syncDirectory(parent); err != nil {
			return closeOnError(fmt.Errorf("sync journal directory: %w", err))
		}
	}
	info, err := file.Stat()
	if err != nil {
		return closeOnError(fmt.Errorf("stat journal: %w", err))
	}
	if err := validateJournalFile(info); err != nil {
		return closeOnError(err)
	}

	store := &Store{file: file, reservePath: path + ".reserve", basePath: path}
	if err := store.load(); err != nil {
		return closeOnError(err)
	}
	if err := store.loadReserve(); err != nil {
		return closeOnError(err)
	}
	if _, err := file.Seek(0, io.SeekEnd); err != nil {
		if store.reserve != nil {
			_ = store.reserve.Close()
		}
		return closeOnError(fmt.Errorf("seek journal: %w", err))
	}
	return store, nil
}

// OpenRotating opens a journal that seals its active file into a numbered
// segment as it fills, so an unattended runner is not stopped by the per-file
// caps after about six weeks.
//
// Sequence numbers and the hash chain stay GLOBAL across segments: reading the
// segments in order and then the active file reproduces exactly the record
// stream a single-file journal would have held. Records() therefore still
// returns the complete history, which is what keeps every fail-closed
// invariant derived by full-scanning it — the halted latch, the clock anchor,
// the daily debit sums — correct across a rotation.
//
// Rotation is deliberately opt-in rather than the default so callers that
// expect one physical file cannot silently begin producing segments. Readers
// that opt in receive the complete logical record stream, including the
// original header and rotation markers.
func OpenRotating(path string) (*Store, error) {
	store, err := open(path, true)
	if err != nil {
		return nil, err
	}
	return store, nil
}

// openRotating opens the stable lock, completes any interrupted rotation, and
// loads every segment in order.
func openRotating(path, parent string) (*Store, error) {
	lock, err := openFile(path + lockSuffix)
	if err != nil {
		return nil, fmt.Errorf("open journal lock: %w", err)
	}
	if err := lockFile(lock); err != nil {
		_ = lock.Close()
		if errors.Is(err, ErrLocked) {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("lock journal: %w", err)
	}
	failed := func(err error) (*Store, error) {
		_ = lock.Close()
		return nil, err
	}
	if err := syncDirectory(parent); err != nil {
		return failed(fmt.Errorf("sync journal directory: %w", err))
	}
	if err := recoverInterruptedRotation(path); err != nil {
		return failed(err)
	}

	segments, err := discoverSegments(path)
	if err != nil {
		return failed(err)
	}
	store := &Store{
		file: nil, reservePath: path + ".reserve",
		rotating: true, basePath: path, lock: lock, segments: len(segments),
	}
	// Sealed segments are immutable: a torn record in one is corruption, not a
	// crash to repair, so they are scanned with no recovery callback.
	seed := scanSeed{}
	for _, segment := range segments {
		records, scanErr := scanSealedSegment(segment, seed)
		if scanErr != nil {
			return failed(scanErr)
		}
		store.records = append(store.records, records...)
		seed = seedFrom(store.records)
	}
	store.activeStart = len(store.records)

	if err := rejectSymlink(path); err != nil {
		return failed(err)
	}
	active, err := openFile(path)
	if err != nil {
		return failed(fmt.Errorf("open journal: %w", err))
	}
	// Persist a newly created active path as well as the stable lock. Syncing
	// the file later does not by itself make its directory entry crash-safe.
	if err := syncDirectory(parent); err != nil {
		_ = active.Close()
		return failed(fmt.Errorf("sync journal directory: %w", err))
	}
	if err := lockFile(active); err != nil {
		_ = active.Close()
		return failed(fmt.Errorf("lock journal: %w", err))
	}
	info, err := active.Stat()
	if err != nil {
		_ = active.Close()
		return failed(fmt.Errorf("stat journal: %w", err))
	}
	if err := validateJournalFile(info); err != nil {
		_ = active.Close()
		return failed(err)
	}
	if len(segments) > 0 && info.Size() == 0 {
		// The staged file was born holding a durable rotation marker before it
		// ever became the active journal, so an empty active file beside
		// sealed segments means the marker was removed. Recreating it here
		// would rewrite history to match the damage.
		_ = active.Close()
		return failed(errors.New("journal active segment is empty; its rotation marker is missing"))
	}
	store.file = active
	if err := store.loadActive(seed); err != nil {
		_ = active.Close()
		return failed(err)
	}
	if err := store.loadReserve(); err != nil {
		_ = active.Close()
		return failed(err)
	}
	if _, err := active.Seek(0, io.SeekEnd); err != nil {
		if store.reserve != nil {
			_ = store.reserve.Close()
		}
		_ = active.Close()
		return failed(fmt.Errorf("seek journal: %w", err))
	}
	// A reserve present at open belongs to an action that was in flight when
	// the process stopped. Rotating now would strand it against the wrong
	// segment, so startup rotation waits for that action to release.
	if store.reserve == nil {
		if err := store.rotateIfFullLocked(); err != nil {
			_ = store.Close()
			return nil, err
		}
	}
	return store, nil
}

// seedFrom derives the continuation seed from the records loaded so far.
func seedFrom(records []Record) scanSeed {
	if len(records) == 0 {
		return scanSeed{}
	}
	tail := records[len(records)-1]
	return scanSeed{startSequence: tail.Sequence, prevHash: tail.Hash, lastAt: tail.At.UTC()}
}

func scanSealedSegment(path string, seed scanSeed) ([]Record, error) {
	if err := rejectSymlink(path); err != nil {
		return nil, err
	}
	file, err := openReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("open journal segment: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat journal segment: %w", err)
	}
	if err := validateJournalFile(info); err != nil {
		return nil, err
	}
	return scanJournalSeeded(file, seed, nil)
}

// loadActive scans the active file as a continuation, repairing only a torn
// final record exactly as the single-file path does.
func (s *Store) loadActive(seed scanSeed) error {
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek journal: %w", err)
	}
	records, err := scanJournalSeeded(s.file, seed, func(lineStart int64) error {
		if err := s.file.Truncate(lineStart); err != nil {
			return fmt.Errorf("recover torn journal tail: %w", err)
		}
		return s.file.Sync()
	})
	if err != nil {
		return err
	}
	if s.segments > 0 && len(records) == 0 {
		return errors.New("journal active segment lost its rotation marker")
	}
	s.records = append(s.records, records...)
	return nil
}

// recoverInterruptedRotation completes or rejects a rotation that a crash left
// half-applied. Rotation stages the new active file first, so the states are
// enumerable and none of them requires inventing a record.
func recoverInterruptedRotation(base string) error {
	staged := base + stagedSuffix
	stagedInfo, stagedErr := os.Lstat(staged)
	if errors.Is(stagedErr, os.ErrNotExist) {
		segments, err := discoverSegments(base)
		if err != nil {
			return err
		}
		if len(segments) == 0 {
			return nil
		}
		if _, err := os.Lstat(base); errors.Is(err, os.ErrNotExist) {
			// Both renames were lost but sealed segments exist. Recreating an
			// active file would silently roll history back to the seal.
			return errors.New("journal active segment is missing; restore it or the sealed segments are incomplete")
		}
		return nil
	}
	if stagedErr != nil {
		return fmt.Errorf("inspect staged journal segment: %w", stagedErr)
	}
	if stagedInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("staged journal segment must not be a symlink")
	}

	segments, err := discoverSegments(base)
	if err != nil {
		return err
	}
	_, baseErr := os.Lstat(base)
	baseMissing := errors.Is(baseErr, os.ErrNotExist)
	if baseErr != nil && !baseMissing {
		return fmt.Errorf("inspect journal: %w", baseErr)
	}
	if baseMissing {
		// The seal rename landed; only the staged promotion was lost.
		if err := verifyStagedMarker(staged, segments, base, true); err != nil {
			return err
		}
		if err := os.Rename(staged, base); err != nil {
			return fmt.Errorf("promote staged journal segment: %w", err)
		}
		return syncDirectory(filepath.Dir(base))
	}
	// Neither rename landed: the staged marker must chain from the active
	// file's current head, or something other than this rotation wrote it.
	if err := verifyStagedMarker(staged, segments, base, false); err != nil {
		return err
	}
	if err := os.Rename(base, segmentPath(base, len(segments)+1)); err != nil {
		return fmt.Errorf("seal journal segment: %w", err)
	}
	if err := syncDirectory(filepath.Dir(base)); err != nil {
		return err
	}
	if err := os.Rename(staged, base); err != nil {
		return fmt.Errorf("promote staged journal segment: %w", err)
	}
	return syncDirectory(filepath.Dir(base))
}

// verifyStagedMarker checks that the staged file holds exactly the rotation
// marker this journal would have written, chained from the records that
// preceded it. Anything else is refused rather than adopted.
func verifyStagedMarker(staged string, segments []string, base string, sealed bool) error {
	seed := scanSeed{}
	for _, segment := range segments {
		records, err := scanSealedSegment(segment, seed)
		if err != nil {
			return err
		}
		seed = seedFrom(records)
		if seed.startSequence == 0 {
			return errors.New("journal segment is empty")
		}
	}
	if !sealed {
		// The active file has not been sealed yet, so the marker must follow
		// its tail rather than the last sealed segment's.
		records, err := scanSealedSegment(base, seed)
		if err != nil {
			return err
		}
		if len(records) == 0 {
			return errors.New("journal active segment is empty; refusing to complete a rotation from it")
		}
		seed = seedFrom(records)
	}
	stagedRecords, err := scanSealedSegment(staged, seed)
	if err != nil {
		return fmt.Errorf("staged journal segment does not continue the chain: %w", err)
	}
	if len(stagedRecords) != 1 || stagedRecords[0].Type != EventRotated {
		return errors.New("staged journal segment does not hold exactly one rotation marker")
	}
	return nil
}

func segmentPath(base string, index int) string {
	return fmt.Sprintf("%s%s%06d", base, segmentSuffix, index)
}

// discoverSegments returns the sealed segment paths in order. Indices must run
// contiguously from one: a gap means a segment was removed, and continuing
// would silently drop the history an invariant depends on.
func discoverSegments(base string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Dir(base))
	if err != nil {
		return nil, fmt.Errorf("read journal directory: %w", err)
	}
	prefix := filepath.Base(base) + segmentSuffix
	found := make(map[int]string)
	highest := 0
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, prefix) {
			continue
		}
		index, convErr := strconv.Atoi(strings.TrimPrefix(name, prefix))
		if convErr != nil || index <= 0 {
			return nil, errors.New("journal directory holds an unrecognized segment name")
		}
		if _, duplicate := found[index]; duplicate {
			return nil, errors.New("journal directory holds a duplicate segment index")
		}
		found[index] = filepath.Join(filepath.Dir(base), name)
		if index > highest {
			highest = index
		}
	}
	if highest > maxSegments {
		return nil, errors.New("journal has more segments than this build supports")
	}
	paths := make([]string, 0, highest)
	for index := 1; index <= highest; index++ {
		path, ok := found[index]
		if !ok {
			return nil, errors.New("journal segments are not contiguous; one has been removed")
		}
		paths = append(paths, path)
	}
	return paths, nil
}

// hasSegments reports whether any rotation artifact exists beside a journal.
func hasSegments(base string) (segments bool, staged bool, err error) {
	entries, readErr := os.ReadDir(filepath.Dir(base))
	if readErr != nil {
		if errors.Is(readErr, os.ErrNotExist) {
			return false, false, nil
		}
		return false, false, fmt.Errorf("read journal directory: %w", readErr)
	}
	prefix := filepath.Base(base) + segmentSuffix
	stagedName := filepath.Base(base) + stagedSuffix
	for _, entry := range entries {
		switch {
		case strings.HasPrefix(entry.Name(), prefix):
			segments = true
		case entry.Name() == stagedName:
			staged = true
		}
	}
	return segments, staged, nil
}

// Verify validates an existing journal without creating it or changing its
// contents. The writer must be stopped so the verifier can hold a shared
// non-blocking lock for the complete read.
func Verify(path string) (Verification, error) {
	_, result, err := readVerified(path)
	return result, err
}

// ReadRecords returns a verified snapshot of an existing journal without
// creating, repairing, or otherwise changing it. It holds the same shared lock
// as Verify for the complete read, so the records and verification describe
// one immutable view rather than two reads joined by a race.
func ReadRecords(path string) ([]Record, error) {
	records, _, err := readVerified(path)
	return records, err
}

func readVerified(path string) ([]Record, Verification, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, Verification{}, errors.New("journal path must be a clean absolute path")
	}
	if err := validateJournalDirectory(filepath.Dir(path)); err != nil {
		return nil, Verification{}, err
	}
	segments, staged, err := hasSegments(path)
	if err != nil {
		return nil, Verification{}, err
	}
	if staged {
		return nil, Verification{}, errors.New("journal has an incomplete rotation; open it once to complete recovery")
	}
	if segments {
		return readVerifiedRotated(path)
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, Verification{}, fmt.Errorf("inspect journal: %w", err)
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, Verification{}, errors.New("journal path must not be a symlink")
	}
	if err := validateJournalFile(before); err != nil {
		return nil, Verification{}, err
	}
	file, err := openReadFile(path)
	if err != nil {
		return nil, Verification{}, fmt.Errorf("open journal read-only: %w", err)
	}
	defer file.Close()
	if err := lockReadFile(file); err != nil {
		return nil, Verification{}, fmt.Errorf("lock journal for verification: %w", err)
	}
	opened, err := file.Stat()
	if err != nil {
		return nil, Verification{}, fmt.Errorf("stat journal: %w", err)
	}
	if !os.SameFile(before, opened) {
		return nil, Verification{}, errors.New("journal changed while opening")
	}
	if err := validateJournalFile(opened); err != nil {
		return nil, Verification{}, err
	}
	hasher := sha256.New()
	limited := &io.LimitedReader{R: file, N: maxJournalBytes + 1}
	records, err := scanJournal(io.TeeReader(limited, hasher), nil)
	if limited.N == 0 {
		return nil, Verification{}, errors.New("journal exceeds size limit")
	}
	if err != nil {
		return nil, Verification{}, err
	}
	final, err := file.Stat()
	if err != nil {
		return nil, Verification{}, fmt.Errorf("stat journal after verification: %w", err)
	}
	if !os.SameFile(opened, final) || final.Size() != opened.Size() ||
		!final.ModTime().Equal(opened.ModTime()) || final.Mode() != opened.Mode() {
		return nil, Verification{}, errors.New("journal changed while verifying")
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(opened, current) {
		return nil, Verification{}, errors.New("journal path changed while verifying")
	}
	sendStarted, submitted := actionEventCounts(records)
	result := Verification{
		Records: len(records), Bytes: final.Size(),
		FileSHA256:         hex.EncodeToString(hasher.Sum(nil)),
		SendStartedRecords: sendStarted,
		SubmittedRecords:   submitted,
	}
	if len(records) > 0 {
		result.ChainHeadSHA256 = records[len(records)-1].Hash
	}
	return records, result, nil
}

// readVerifiedRotated walks the sealed segments in order and then the active file,
// validating one continuous chain. It holds a shared lock on the stable lock
// file, which the writer holds exclusively, so a successful verification still
// proves no writer was active for the whole read — the guarantee that would
// otherwise be lost once the active file's identity can change.
//
// The digest covers the segments' bytes in index order followed by the active
// file, so it is reproducible by concatenating the files in that order.
func readVerifiedRotated(path string) ([]Record, Verification, error) {
	lock, err := openReadFile(path + lockSuffix)
	if err != nil {
		return nil, Verification{}, errors.New("journal lock file is missing; the journal has never been opened for rotation")
	}
	defer lock.Close()
	if err := lockReadFile(lock); err != nil {
		return nil, Verification{}, fmt.Errorf("lock journal for verification: %w", err)
	}
	segments, err := discoverSegments(path)
	if err != nil {
		return nil, Verification{}, err
	}
	hasher := sha256.New()
	var (
		all   []Record
		bytes int64
		seed  scanSeed
	)
	for _, file := range append(append([]string{}, segments...), path) {
		records, size, scanErr := verifySegmentFile(file, seed, hasher)
		if scanErr != nil {
			return nil, Verification{}, scanErr
		}
		all = append(all, records...)
		bytes += size
		seed = seedFrom(all)
	}
	sendStarted, submitted := actionEventCounts(all)
	result := Verification{
		Records: len(all), Bytes: bytes,
		FileSHA256:         hex.EncodeToString(hasher.Sum(nil)),
		SendStartedRecords: sendStarted,
		SubmittedRecords:   submitted,
	}
	if len(all) > 0 {
		result.ChainHeadSHA256 = all[len(all)-1].Hash
	}
	return all, result, nil
}

func verifySegmentFile(
	path string, seed scanSeed, hasher io.Writer,
) ([]Record, int64, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, 0, fmt.Errorf("inspect journal segment: %w", err)
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, 0, errors.New("journal segment must not be a symlink")
	}
	if err := validateJournalFile(before); err != nil {
		return nil, 0, err
	}
	file, err := openReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("open journal segment: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("stat journal segment: %w", err)
	}
	if !os.SameFile(before, opened) {
		return nil, 0, errors.New("journal segment changed while opening")
	}
	limited := &io.LimitedReader{R: file, N: maxJournalBytes + 1}
	records, err := scanJournalSeeded(io.TeeReader(limited, hasher), seed, nil)
	if limited.N == 0 {
		return nil, 0, errors.New("journal segment exceeds size limit")
	}
	if err != nil {
		return nil, 0, err
	}
	final, err := file.Stat()
	if err != nil {
		return nil, 0, fmt.Errorf("stat journal segment after verification: %w", err)
	}
	if !os.SameFile(opened, final) || final.Size() != opened.Size() ||
		!final.ModTime().Equal(opened.ModTime()) || final.Mode() != opened.Mode() {
		return nil, 0, errors.New("journal segment changed while verifying")
	}
	return records, final.Size(), nil
}

func validateJournalDirectory(path string) error {
	if err := secureexec.ValidateProtectedDirectory(path); err != nil {
		return errors.New("journal directory ancestry is not trusted")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect journal directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return errors.New("journal directory must be a directory, not a symlink")
	}
	if !fileowner.Trusted(info) {
		return errors.New("journal directory owner is not trusted")
	}
	if info.Mode().Perm()&0o022 != 0 {
		return errors.New("journal directory must not be group- or world-writable")
	}
	return nil
}

func validateJournalFile(info os.FileInfo) error {
	if !info.Mode().IsRegular() || !fileowner.Trusted(info) || info.Mode().Perm()&0o077 != 0 {
		return errors.New("journal must be a private regular file")
	}
	if info.Size() > maxJournalBytes {
		return errors.New("journal exceeds size limit")
	}
	return nil
}

func rejectSymlink(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect journal: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return errors.New("journal path must not be a symlink")
	}
	return nil
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (s *Store) load() error {
	if _, err := s.file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek journal: %w", err)
	}
	records, err := scanJournal(s.file, func(lineStart int64) error {
		if err := s.file.Truncate(lineStart); err != nil {
			return fmt.Errorf("recover torn journal tail: %w", err)
		}
		if err := s.file.Sync(); err != nil {
			return fmt.Errorf("sync recovered journal: %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	s.records = records
	return nil
}

func scanJournal(source io.Reader, recoverTorn func(int64) error) ([]Record, error) {
	return scanJournalSeeded(source, scanSeed{}, recoverTorn)
}

// scanJournalSeeded validates one file as a continuation of a chain that began
// in an earlier segment. With a zero seed it is exactly the single-file scan.
// The per-file record and byte caps stay per-file: lifting the aggregate limit
// is the whole point of segmenting.
func scanJournalSeeded(
	source io.Reader, seed scanSeed, recoverTorn func(int64) error,
) ([]Record, error) {
	reader := bufio.NewReaderSize(source, 64<<10)
	var (
		records  []Record
		offset   int64
		prevHash = seed.prevHash
		lastAt   = seed.lastAt
	)
	for {
		line, err := reader.ReadBytes('\n')
		lineStart := offset
		offset += int64(len(line))
		if len(line) > maxRecordBytes {
			return nil, errors.New("journal record exceeds size limit")
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("read journal: %w", err)
		}
		if len(line) == 0 && errors.Is(err, io.EOF) {
			break
		}
		if !bytes.HasSuffix(line, []byte{'\n'}) {
			if !errors.Is(err, io.EOF) {
				return nil, errors.New("journal record is not newline terminated")
			}
			if recoverTorn == nil {
				return nil, errors.New("journal has an incomplete final record")
			}
			if recoverErr := recoverTorn(lineStart); recoverErr != nil {
				return nil, recoverErr
			}
			break
		}
		line = bytes.TrimSuffix(line, []byte{'\n'})
		var record Record
		if err := strictjson.Decode(line, &record); err != nil {
			return nil, fmt.Errorf("decode journal record %d: %w", len(records)+1, err)
		}
		if record.Sequence != seed.startSequence+uint64(len(records))+1 {
			return nil, errors.New("journal sequence is not contiguous")
		}
		if record.Type == "" || record.At.IsZero() || len(record.Payload) == 0 {
			return nil, errors.New("journal record is missing required fields")
		}
		if !lastAt.IsZero() && record.At.Before(lastAt) {
			return nil, errors.New("journal record time regressed")
		}
		if record.PrevHash != prevHash {
			return nil, errors.New("journal hash chain is discontinuous")
		}
		want, hashErr := recordHash(record)
		if hashErr != nil {
			return nil, hashErr
		}
		if record.Hash != want {
			return nil, errors.New("journal record hash does not match")
		}
		records = append(records, record)
		if len(records) > maxRecords {
			return nil, errors.New("journal exceeds record limit")
		}
		prevHash = record.Hash
		lastAt = record.At.UTC()
		if errors.Is(err, io.EOF) {
			break
		}
	}
	return records, nil
}

func (s *Store) Records() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, len(s.records))
	for i := range s.records {
		out[i] = s.records[i]
		out[i].Payload = bytes.Clone(s.records[i].Payload)
	}
	return out
}

func (s *Store) Stats() (Stats, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return Stats{}, errors.New("journal is closed")
	}
	info, err := s.file.Stat()
	if err != nil {
		return Stats{}, fmt.Errorf("stat journal: %w", err)
	}
	sendStarted, submitted := actionEventCounts(s.records)
	return Stats{
		ActiveRecords:      len(s.records) - s.activeStart,
		Records:            len(s.records),
		Bytes:              info.Size(),
		ReservedBytes:      s.reserveBytes,
		MaxRecords:         maxRecords,
		MaxBytes:           maxJournalBytes,
		SendStartedRecords: sendStarted,
		SubmittedRecords:   submitted,
	}, nil
}

func actionEventCounts(records []Record) (sendStarted, submitted int) {
	for _, record := range records {
		switch record.Type {
		case "swap.send_started", "transaction.send_started":
			sendStarted++
		case "swap.submitted", "transaction.submitted":
			submitted++
		}
	}
	return sendStarted, submitted
}

// EnsureCapacity reserves filesystem space for the current single-writer
// execution's remaining bounded transitions.
func (s *Store) EnsureCapacity(records int, bytes int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return errors.New("journal is closed")
	}
	if s.poison != nil {
		return fmt.Errorf("journal requires reopen after an append failure: %w", s.poison)
	}
	if records <= 0 || bytes <= 0 {
		return errors.New("journal capacity request must be positive")
	}
	if s.activeRecords()+records > maxRecords {
		return errors.New("journal lacks record capacity")
	}
	info, err := s.file.Stat()
	if err != nil {
		return fmt.Errorf("stat journal: %w", err)
	}
	if info.Size()+bytes > maxJournalBytes {
		return errors.New("journal lacks byte capacity")
	}
	if err := s.openReserveLocked(); err != nil {
		return err
	}
	if s.reserveBytes >= bytes {
		return nil
	}
	previous := s.reserveBytes
	if _, err := s.reserve.Seek(0, io.SeekEnd); err != nil {
		return fmt.Errorf("seek journal reserve: %w", err)
	}
	if err := writeZeros(s.reserve, bytes-previous); err != nil {
		_ = s.reserve.Truncate(previous)
		_ = s.reserve.Sync()
		return fmt.Errorf("reserve journal capacity: %w", err)
	}
	if err := s.reserve.Sync(); err != nil {
		_ = s.reserve.Truncate(previous)
		_ = s.reserve.Sync()
		return fmt.Errorf("sync journal reserve: %w", err)
	}
	s.reserveBytes = bytes
	return nil
}

// ReleaseCapacity removes any unused filesystem reservation after a terminal
// transition is durable.
func (s *Store) ReleaseCapacity() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.releaseCapacityLocked(); err != nil {
		return err
	}
	// Rotation happens here and nowhere else. Every caller of ReleaseCapacity
	// has just finished an action: nothing is in flight, the reservation is
	// gone, and a halted action that still needs acknowledgement deliberately
	// never reaches this call. That makes this the one point where sealing the
	// active file cannot cut across a live transaction.
	return s.rotateIfFullLocked()
}

// activeRecords is the number of records in the active segment. The per-file
// caps apply to it; sequence and hash chain remain global.
func (s *Store) activeRecords() int {
	return len(s.records) - s.activeStart
}

// rotateIfFullLocked seals the active file once it passes half a segment's
// worth. Half rather than full leaves room for the terminal records an action
// in progress may still need to write before its next release.
func (s *Store) rotateIfFullLocked() error {
	if !s.rotating || s.file == nil || s.poison != nil {
		return nil
	}
	if s.activeRecords() < maxRecords/2 {
		info, err := s.file.Stat()
		if err != nil {
			return fmt.Errorf("stat journal: %w", err)
		}
		if info.Size() < maxJournalBytes/2 {
			return nil
		}
	}
	if s.segments >= maxSegments {
		return errors.New("journal has reached its segment limit; archive the sealed segments")
	}
	return s.rotateLocked()
}

// rotateLocked seals the active file and promotes a staged successor.
//
// The successor is created and given its rotation marker BEFORE either rename,
// so at every instant on disk the complete chain is recoverable: the marker
// names the segment it follows and carries its chain head, and it is durable
// before the file bearing it becomes the journal. A crash between the renames
// leaves exactly one recoverable state, handled at open.
//
// Failure at or after the first rename poisons the store: three callers
// discard this method's error so that a cleanup failure cannot hide a durable
// terminal result, which means a half-applied rotation must stop later appends
// by itself rather than relying on the caller noticing.
func (s *Store) rotateLocked() error {
	staged := s.basePath + stagedSuffix
	next, err := createExclusive(staged)
	if err != nil {
		return fmt.Errorf("stage journal segment: %w", err)
	}
	cleanupStaged := func(cause error) error {
		_ = next.Close()
		_ = os.Remove(staged)
		return cause
	}
	if err := lockFile(next); err != nil {
		return cleanupStaged(fmt.Errorf("lock staged journal segment: %w", err))
	}
	tail := s.records[len(s.records)-1]
	marker := Record{
		Sequence: tail.Sequence + 1,
		At:       tail.At.UTC(),
		Type:     EventRotated,
	}
	payload, err := json.Marshal(rotationMarker{
		SealedSegment:      s.segments + 1,
		SealedLastSequence: tail.Sequence,
		SealedChainHead:    tail.Hash,
	})
	if err != nil {
		return cleanupStaged(errors.New("encode journal rotation marker"))
	}
	marker.Payload = payload
	marker.PrevHash = tail.Hash
	marker.Hash, err = recordHash(marker)
	if err != nil {
		return cleanupStaged(err)
	}
	line, err := json.Marshal(marker)
	if err != nil {
		return cleanupStaged(errors.New("encode journal rotation marker record"))
	}
	line = append(line, '\n')
	if err := writeAll(next, line); err != nil {
		return cleanupStaged(fmt.Errorf("write journal rotation marker: %w", err))
	}
	if err := next.Sync(); err != nil {
		return cleanupStaged(fmt.Errorf("sync journal rotation marker: %w", err))
	}
	parent := filepath.Dir(s.basePath)
	if err := syncDirectory(parent); err != nil {
		return cleanupStaged(err)
	}

	// From here a failure leaves the journal half-rotated on disk. Recovery at
	// open completes it; this process must stop appending.
	poison := func(cause error) error {
		s.poison = cause
		_ = next.Close()
		return cause
	}
	sealed := segmentPath(s.basePath, s.segments+1)
	if err := os.Rename(s.basePath, sealed); err != nil {
		return poison(fmt.Errorf("seal journal segment: %w", err))
	}
	if err := syncDirectory(parent); err != nil {
		return poison(err)
	}
	if err := os.Rename(staged, s.basePath); err != nil {
		return poison(fmt.Errorf("promote staged journal segment: %w", err))
	}
	if err := syncDirectory(parent); err != nil {
		return poison(err)
	}
	if err := s.file.Close(); err != nil {
		return poison(fmt.Errorf("close sealed journal segment: %w", err))
	}
	if _, err := next.Seek(0, io.SeekEnd); err != nil {
		return poison(fmt.Errorf("seek journal: %w", err))
	}
	s.file = next
	s.segments++
	s.records = append(s.records, marker)
	s.activeStart = len(s.records) - 1
	return nil
}

func (s *Store) Append(at time.Time, eventType, actionID string, payload any) (Record, error) {
	return s.append(at, eventType, actionID, payload, true)
}

// AppendBuffered appends a record without syncing it to stable storage. It is
// only for replayable bulk inputs whose caller calls Sync before acknowledging
// progress. Security-sensitive state transitions must continue to use Append.
func (s *Store) AppendBuffered(at time.Time, eventType, actionID string, payload any) (Record, error) {
	return s.append(at, eventType, actionID, payload, false)
}

func (s *Store) append(at time.Time, eventType, actionID string, payload any, syncRecord bool) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return Record{}, errors.New("journal is closed")
	}
	if s.poison != nil {
		return Record{}, fmt.Errorf("journal requires reopen after an append failure: %w", s.poison)
	}
	if eventType == "" || at.IsZero() {
		return Record{}, errors.New("event type and time are required")
	}
	at = at.UTC()
	if len(s.records) > 0 && at.Before(s.records[len(s.records)-1].At) {
		return Record{}, errors.New("journal event time regressed")
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return Record{}, fmt.Errorf("encode journal payload: %w", err)
	}
	// Idle records do not reserve action capacity, so ReleaseCapacity is never
	// called for them. Rotate here only when no action is in flight; an open
	// reserve keeps the current segment stable through its terminal records.
	if s.rotating && s.reserve == nil {
		if err := s.rotateIfFullLocked(); err != nil {
			return Record{}, err
		}
	}
	if s.activeRecords() >= maxRecords {
		return Record{}, errors.New("journal record limit reached")
	}
	record := Record{
		Sequence: uint64(len(s.records) + 1),
		At:       at,
		Type:     eventType,
		ActionID: actionID,
		Payload:  encodedPayload,
	}
	if len(s.records) > 0 {
		record.PrevHash = s.records[len(s.records)-1].Hash
	}
	record.Hash, err = recordHash(record)
	if err != nil {
		return Record{}, err
	}
	line, err := json.Marshal(record)
	if err != nil {
		return Record{}, fmt.Errorf("encode journal record: %w", err)
	}
	line = append(line, '\n')
	if len(line) > maxRecordBytes {
		return Record{}, errors.New("journal record exceeds size limit")
	}
	info, err := s.file.Stat()
	if err != nil {
		return Record{}, fmt.Errorf("stat journal: %w", err)
	}
	if info.Size()+int64(len(line)) > maxJournalBytes {
		return Record{}, errors.New("journal size limit reached")
	}
	if err := s.consumeReservationLocked(int64(len(line))); err != nil {
		return Record{}, err
	}
	if err := writeAll(s.file, line); err != nil {
		s.poison = err
		if truncateErr := s.file.Truncate(info.Size()); truncateErr == nil {
			_ = s.file.Sync()
		}
		return Record{}, fmt.Errorf("append journal: %w", err)
	}
	if syncRecord {
		if err := s.file.Sync(); err != nil {
			s.poison = err
			return Record{}, fmt.Errorf("sync journal: %w", err)
		}
	}
	s.records = append(s.records, record)
	return record, nil
}

// Sync makes preceding AppendBuffered records durable before their caller
// reports a durable cursor or accepts more than its bounded replay window.
func (s *Store) Sync() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return errors.New("journal is closed")
	}
	if s.poison != nil {
		return fmt.Errorf("journal requires reopen after an append failure: %w", s.poison)
	}
	if err := s.file.Sync(); err != nil {
		s.poison = err
		return fmt.Errorf("sync journal: %w", err)
	}
	return nil
}

func recordHash(record Record) (string, error) {
	unsigned := unsignedRecord{
		Domain:   hashDomain,
		Sequence: record.Sequence,
		At:       record.At.UTC(),
		Type:     record.Type,
		ActionID: record.ActionID,
		Payload:  record.Payload,
		PrevHash: record.PrevHash,
	}
	encoded, err := json.Marshal(unsigned)
	if err != nil {
		return "", fmt.Errorf("encode journal hash input: %w", err)
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}

func writeAll(writer io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := writer.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}

func writeZeros(writer io.Writer, bytes int64) error {
	zeroes := make([]byte, 64<<10)
	for bytes > 0 {
		chunk := int64(len(zeroes))
		if bytes < chunk {
			chunk = bytes
		}
		if err := writeAll(writer, zeroes[:int(chunk)]); err != nil {
			return err
		}
		bytes -= chunk
	}
	return nil
}

func (s *Store) loadReserve() error {
	info, err := os.Lstat(s.reservePath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect journal reserve: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
		!fileowner.Trusted(info) || info.Mode().Perm()&0o077 != 0 ||
		info.Size() > maxJournalBytes {
		return errors.New("journal reserve must be a bounded private regular file")
	}
	file, err := openFile(s.reservePath)
	if err != nil {
		return fmt.Errorf("open journal reserve: %w", err)
	}
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() ||
		!fileowner.Trusted(openedInfo) ||
		openedInfo.Mode().Perm()&0o077 != 0 ||
		openedInfo.Size() > maxJournalBytes {
		_ = file.Close()
		return errors.New("journal reserve changed while opening")
	}
	s.reserve = file
	s.reserveBytes = openedInfo.Size()
	return nil
}

func (s *Store) openReserveLocked() error {
	if s.reserve != nil {
		return nil
	}
	_, statErr := os.Lstat(s.reservePath)
	created := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !created {
		return fmt.Errorf("inspect journal reserve: %w", statErr)
	}
	if !created {
		return errors.New("journal reserve disappeared while open")
	}
	file, err := openFile(s.reservePath)
	if err != nil {
		return fmt.Errorf("create journal reserve: %w", err)
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || !fileowner.Trusted(info) ||
		info.Mode().Perm()&0o077 != 0 {
		_ = file.Close()
		_ = os.Remove(s.reservePath)
		return errors.New("journal reserve must be a private regular file")
	}
	if err := syncDirectory(filepath.Dir(s.reservePath)); err != nil {
		_ = file.Close()
		_ = os.Remove(s.reservePath)
		return fmt.Errorf("sync journal reserve directory: %w", err)
	}
	s.reserve = file
	s.reserveBytes = 0
	return nil
}

func (s *Store) consumeReservationLocked(bytes int64) error {
	if s.reserve == nil || bytes <= 0 {
		return nil
	}
	remaining := s.reserveBytes - bytes
	if remaining < 0 {
		remaining = 0
	}
	if err := s.reserve.Truncate(remaining); err != nil {
		return fmt.Errorf("release journal reserve: %w", err)
	}
	if err := s.reserve.Sync(); err != nil {
		return fmt.Errorf("sync journal reserve: %w", err)
	}
	s.reserveBytes = remaining
	return nil
}

func (s *Store) releaseCapacityLocked() error {
	if s.reserve == nil {
		return nil
	}
	closeErr := s.reserve.Close()
	s.reserve = nil
	s.reserveBytes = 0
	removeErr := os.Remove(s.reservePath)
	if errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	syncErr := syncDirectory(filepath.Dir(s.reservePath))
	if closeErr != nil {
		return fmt.Errorf("close journal reserve: %w", closeErr)
	}
	if removeErr != nil {
		return fmt.Errorf("remove journal reserve: %w", removeErr)
	}
	if syncErr != nil {
		return fmt.Errorf("sync journal reserve directory: %w", syncErr)
	}
	return nil
}

func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return nil
	}
	var reserveErr error
	if s.reserve != nil {
		reserveErr = s.reserve.Close()
		s.reserve = nil
	}
	err := s.file.Close()
	s.file = nil
	if s.lock != nil {
		if lockErr := s.lock.Close(); err == nil {
			err = lockErr
		}
		s.lock = nil
	}
	if err == nil {
		err = reserveErr
	}
	return err
}
