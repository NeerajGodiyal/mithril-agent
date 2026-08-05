package telegramoperator

import (
	"encoding/json"
	"errors"
	"os"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
)

const (
	cursorVersion  = 1
	maxCursorBytes = 256
)

type cursorDocument struct {
	Version    uint32 `json:"version"`
	NextOffset int64  `json:"next_offset"`
}

// FileCursor is a small private atomic cursor. The service remains the only
// writer; the monotonic check additionally fails closed on stale writes.
type FileCursor string

func (c FileCursor) lockConsumer() (func(), error) {
	lock, err := acquirePrivateFileLock(string(c) + ".lock")
	if err != nil {
		if errors.Is(err, errPrivateLockHeld) {
			return nil, errors.New("Telegram update consumer is already active")
		}
		return nil, errors.New("Telegram update consumer lock is unavailable")
	}
	return func() { _ = lock.Close() }, nil
}

func (c FileCursor) Load() (int64, error) {
	document, err := readCursor(string(c))
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return document.NextOffset, nil
}

func (c FileCursor) Store(nextOffset int64) error {
	if nextOffset <= 0 {
		return errors.New("Telegram update cursor must be positive")
	}
	current, err := readCursor(string(c))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err == nil {
		if nextOffset < current.NextOffset {
			return errors.New("Telegram update cursor cannot move backwards")
		}
		if nextOffset == current.NextOffset {
			return nil
		}
	}
	encoded, err := json.Marshal(cursorDocument{
		Version: cursorVersion, NextOffset: nextOffset,
	})
	if err != nil {
		return errors.New("encode Telegram update cursor")
	}
	if err := securefile.ReplacePrivate(
		string(c), append(encoded, '\n'), maxCursorBytes,
	); err != nil {
		return errors.New("write Telegram update cursor")
	}
	return nil
}

func readCursor(path string) (cursorDocument, error) {
	data, err := securefile.ReadPrivate(path, maxCursorBytes)
	if err != nil {
		return cursorDocument{}, err
	}
	var document cursorDocument
	if err := strictjson.Decode(data, &document); err != nil {
		return cursorDocument{}, errors.New("decode Telegram update cursor")
	}
	if document.Version != cursorVersion || document.NextOffset <= 0 {
		return cursorDocument{}, errors.New("Telegram update cursor is invalid")
	}
	return document, nil
}
