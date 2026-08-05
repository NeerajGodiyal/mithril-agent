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
)

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
}

type Stats struct {
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

	store := &Store{file: file, reservePath: path + ".reserve"}
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

// Verify validates an existing journal without creating it or changing its
// contents. The writer must be stopped so the verifier can hold a shared
// non-blocking lock for the complete read.
func Verify(path string) (Verification, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return Verification{}, errors.New("journal path must be a clean absolute path")
	}
	if err := validateJournalDirectory(filepath.Dir(path)); err != nil {
		return Verification{}, err
	}
	before, err := os.Lstat(path)
	if err != nil {
		return Verification{}, fmt.Errorf("inspect journal: %w", err)
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return Verification{}, errors.New("journal path must not be a symlink")
	}
	if err := validateJournalFile(before); err != nil {
		return Verification{}, err
	}
	file, err := openReadFile(path)
	if err != nil {
		return Verification{}, fmt.Errorf("open journal read-only: %w", err)
	}
	defer file.Close()
	if err := lockReadFile(file); err != nil {
		return Verification{}, fmt.Errorf("lock journal for verification: %w", err)
	}
	opened, err := file.Stat()
	if err != nil {
		return Verification{}, fmt.Errorf("stat journal: %w", err)
	}
	if !os.SameFile(before, opened) {
		return Verification{}, errors.New("journal changed while opening")
	}
	if err := validateJournalFile(opened); err != nil {
		return Verification{}, err
	}
	hasher := sha256.New()
	limited := &io.LimitedReader{R: file, N: maxJournalBytes + 1}
	records, err := scanJournal(io.TeeReader(limited, hasher), nil)
	if limited.N == 0 {
		return Verification{}, errors.New("journal exceeds size limit")
	}
	if err != nil {
		return Verification{}, err
	}
	final, err := file.Stat()
	if err != nil {
		return Verification{}, fmt.Errorf("stat journal after verification: %w", err)
	}
	if !os.SameFile(opened, final) || final.Size() != opened.Size() ||
		!final.ModTime().Equal(opened.ModTime()) || final.Mode() != opened.Mode() {
		return Verification{}, errors.New("journal changed while verifying")
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(opened, current) {
		return Verification{}, errors.New("journal path changed while verifying")
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
	return result, nil
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
	reader := bufio.NewReaderSize(source, 64<<10)
	var (
		records  []Record
		offset   int64
		prevHash string
		lastAt   time.Time
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
		if record.Sequence != uint64(len(records)+1) {
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
	if len(s.records)+records > maxRecords {
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
	return s.releaseCapacityLocked()
}

func (s *Store) Append(at time.Time, eventType, actionID string, payload any) (Record, error) {
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
	if len(s.records) >= maxRecords {
		return Record{}, errors.New("journal record limit reached")
	}
	encodedPayload, err := json.Marshal(payload)
	if err != nil {
		return Record{}, fmt.Errorf("encode journal payload: %w", err)
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
	if err := s.file.Sync(); err != nil {
		s.poison = err
		return Record{}, fmt.Errorf("sync journal: %w", err)
	}
	s.records = append(s.records, record)
	return record, nil
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
	if err == nil {
		err = reserveErr
	}
	return err
}
