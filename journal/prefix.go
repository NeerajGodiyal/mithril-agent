package journal

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ErrPrefixChanged reports that journal rotation changed the visible file
// layout while a durable prefix was being opened. Callers may retry with the
// same descriptor because rotation preserves the logical byte stream.
var ErrPrefixChanged = errors.New("journal durable prefix changed while reading")

// DurablePrefix identifies an fsynced prefix of the logical journal stream.
// Bytes spans sealed segments followed by the active file; Records and the
// chain head make the descriptor independently verifiable by a reader.
type DurablePrefix struct {
	Format          string `json:"format"`
	Bytes           int64  `json:"bytes"`
	Records         int    `json:"records"`
	ChainHeadSHA256 string `json:"chain_head_sha256"`
}

// DurablePrefix syncs the active file and returns the complete durable logical
// prefix. Rotation does not invalidate an older prefix because segment bytes
// are concatenated in the same order as the original active file.
func (s *Store) DurablePrefix() (DurablePrefix, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.file == nil {
		return DurablePrefix{}, errors.New("journal is closed")
	}
	if s.poison != nil {
		return DurablePrefix{}, fmt.Errorf("journal requires reopen after an append failure: %w", s.poison)
	}
	if len(s.records) == 0 {
		return DurablePrefix{}, errors.New("journal has no durable records")
	}
	if err := s.file.Sync(); err != nil {
		s.poison = err
		return DurablePrefix{}, fmt.Errorf("sync journal: %w", err)
	}
	var bytes int64
	for index := 1; index <= s.segments; index++ {
		info, err := os.Lstat(segmentPath(s.basePath, index))
		if err != nil {
			return DurablePrefix{}, fmt.Errorf("inspect journal segment: %w", err)
		}
		if err := validateJournalFile(info); err != nil {
			return DurablePrefix{}, err
		}
		bytes += info.Size()
	}
	info, err := s.file.Stat()
	if err != nil {
		return DurablePrefix{}, fmt.Errorf("stat journal: %w", err)
	}
	if err := validateJournalFile(info); err != nil {
		return DurablePrefix{}, err
	}
	bytes += info.Size()
	tail := s.records[len(s.records)-1]
	return DurablePrefix{
		Format: Format, Bytes: bytes, Records: len(s.records), ChainHeadSHA256: tail.Hash,
	}, nil
}

// ReadDurablePrefix verifies a previously published journal prefix without
// taking the writer lock. It never reads bytes appended after the descriptor.
func ReadDurablePrefix(path string, prefix DurablePrefix) ([]Record, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("journal path must be a clean absolute path")
	}
	if err := validateJournalDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	if err := validateDurablePrefix(prefix); err != nil {
		return nil, err
	}
	segments, err := discoverSegments(path)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPrefixChanged, err)
	}
	paths := append(segments, path)
	remaining := prefix.Bytes
	var (
		records []Record
		seed    scanSeed
	)
	for _, current := range paths {
		if remaining == 0 {
			break
		}
		part, consumed, err := readPrefixFile(current, remaining, seed)
		if err != nil {
			return nil, err
		}
		records = append(records, part...)
		remaining -= consumed
		seed = seedFrom(records)
	}
	if remaining != 0 {
		return nil, ErrPrefixChanged
	}
	if len(records) != prefix.Records || len(records) == 0 ||
		records[len(records)-1].Hash != prefix.ChainHeadSHA256 {
		return nil, errors.New("journal durable prefix does not match its record count or chain head")
	}
	return records, nil
}

func validateDurablePrefix(prefix DurablePrefix) error {
	if prefix.Format != Format || prefix.Bytes <= 0 ||
		prefix.Bytes > int64(maxSegments+1)*maxJournalBytes ||
		prefix.Records <= 0 || prefix.Records > (maxSegments+1)*maxRecords ||
		len(prefix.ChainHeadSHA256) != 2*32 ||
		strings.ToLower(prefix.ChainHeadSHA256) != prefix.ChainHeadSHA256 {
		return errors.New("journal durable prefix descriptor is invalid")
	}
	if _, err := hex.DecodeString(prefix.ChainHeadSHA256); err != nil {
		return errors.New("journal durable prefix descriptor is invalid")
	}
	return nil
}

func readPrefixFile(path string, remaining int64, seed scanSeed) ([]Record, int64, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: inspect journal prefix file", ErrPrefixChanged)
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, 0, errors.New("journal prefix file must not be a symlink")
	}
	if err := validateJournalFile(before); err != nil {
		return nil, 0, err
	}
	file, err := openReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("%w: open journal prefix file", ErrPrefixChanged)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, 0, ErrPrefixChanged
	}
	take := min(remaining, opened.Size())
	if take <= 0 {
		return nil, 0, ErrPrefixChanged
	}
	limited := &io.LimitedReader{R: file, N: take}
	records, err := scanJournalSeeded(limited, seed, nil)
	if err != nil {
		return nil, 0, fmt.Errorf("verify journal durable prefix: %w", err)
	}
	if limited.N != 0 {
		return nil, 0, ErrPrefixChanged
	}
	final, err := file.Stat()
	if err != nil || !os.SameFile(opened, final) || final.Size() < take || final.Mode() != opened.Mode() {
		return nil, 0, ErrPrefixChanged
	}
	return records, take, nil
}
