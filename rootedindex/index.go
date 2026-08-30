// Package rootedindex persists and queries deterministic rooted program events
// emitted by a Mithril node. It never contacts a network or loads a wallet.
package rootedindex

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/solana"
	solanago "github.com/gagliardetto/solana-go"
	"github.com/zeebo/blake3"
)

const (
	EventSchemaVersion = 3
	IndexSchemaVersion = 5
	// SchemaVersion remains the rooted-event wire version for callers that
	// construct events. Index journals have their own, independently versioned
	// schema because source binding does not change Mithril's event payloads.
	SchemaVersion                    = EventSchemaVersion
	AlpenglowRootedProvenance        = "mithril_alpenglow_rooted_feed"
	ClassicFinalizedRootedProvenance = "mithril_classic_finalized_feed"
	// RootedProvenance remains the native Alpenglow label for compatibility.
	// New output must derive provenance from the bound source cluster.
	RootedProvenance          = AlpenglowRootedProvenance
	RootedFinality            = "rooted"
	SourceRecordType          = "mithril.rooted_source"
	StartRecordType           = "mithril.rooted_start"
	BatchRecordType           = "mithril.rooted_batch"
	SupportedSidecarVersion   = 2
	maxAccountDataBytes       = 10 << 20
	maxEventBytes             = 16 << 20
	maxLegacyTransactionBytes = 1232
	maxLogBytes               = 10_064
	maxReturnDataBytes        = 1024
	querySnapshotVersion      = 1
	maxQuerySnapshotBytes     = 4 << 10
	querySnapshotName         = "query-snapshot.json"

	eventHeader      = "rooted_index.header"
	eventStart       = "rooted_index.start"
	eventBatch       = "rooted_index.batch"
	eventTransaction = "rooted_index.transaction"
	eventAccount     = "rooted_index.account"
	eventRoot        = "rooted_index.root"
)

var (
	errV4Index         = errors.New("rooted index schema v4 lacks transaction-v1 identity and full root lineage; preserve it for audit and rebuild a private v5 index from Mithril's event-schema-v3 framed rooted feed")
	errSourceLessIndex = errors.New("rooted index schema lacks the current event, source, or stream-start binding; preserve it for audit and rebuild a private v5 index from Mithril's event-schema-v3 framed rooted feed")
)

type TransactionVersion string

const (
	TransactionVersionLegacy TransactionVersion = "legacy"
	TransactionVersionV0     TransactionVersion = "v0"
	TransactionVersionV1     TransactionVersion = "v1"
)

type FinalitySource string

const (
	FinalityAlpenglowCertificate FinalitySource = "alpenglow_certificate"
	FinalityAlpenglowDelegated   FinalitySource = "alpenglow_delegated"
	FinalityRPCFinalized         FinalitySource = "rpc_finalized"
)

type SourceDescriptor struct {
	Cluster             string `json:"cluster"`
	GenesisHash         string `json:"genesis_hash"`
	AccountsDBRootRunID string `json:"accountsdb_root_run_id"`
}

type BatchDescriptor struct {
	ManifestSequence uint64 `json:"manifest_sequence"`
	SidecarVersion   uint32 `json:"sidecar_version"`
	FromSlot         uint64 `json:"from_slot"`
	ThroughSlot      uint64 `json:"through_slot"`
	SHA256           string `json:"sha256"`
}

type sourceFrame struct {
	RecordType string           `json:"record_type"`
	Source     SourceDescriptor `json:"source"`
}

type StartDescriptor struct {
	After *Cursor `json:"after"`
}

type startFrame struct {
	RecordType string           `json:"record_type"`
	Start      *StartDescriptor `json:"start"`
}

type batchFrame struct {
	RecordType string          `json:"record_type"`
	Batch      BatchDescriptor `json:"batch"`
}

type Filter struct {
	Owner   string `json:"owner,omitempty"`
	Account string `json:"account,omitempty"`
	Mention string `json:"mention,omitempty"`
}

type Cursor struct {
	Slot    uint64 `json:"slot"`
	Ordinal uint32 `json:"ordinal"`
}

func (c Cursor) String() string { return fmt.Sprintf("%d:%d", c.Slot, c.Ordinal) }

type AccountUpdate struct {
	Pubkey     string `json:"pubkey"`
	Owner      string `json:"owner"`
	Lamports   uint64 `json:"lamports"`
	Executable bool   `json:"executable"`
	RentEpoch  uint64 `json:"rent_epoch"`
	Data       []byte `json:"data"`
	Tombstone  bool   `json:"tombstone"`
}

type CompiledInstruction struct {
	ProgramIDIndex uint16   `json:"program_id_index"`
	Accounts       []uint16 `json:"accounts"`
	Data           []byte   `json:"data"`
}

type InnerInstructions struct {
	Index        uint8                 `json:"index"`
	Instructions []CompiledInstruction `json:"instructions"`
}

type ReturnData struct {
	ProgramID string `json:"program_id"`
	Data      []byte `json:"data"`
}

type Transaction struct {
	Index         uint32              `json:"index"`
	Signature     string              `json:"signature"`
	Transaction   []byte              `json:"transaction"`
	MessageHash   string              `json:"message_hash"`
	AccountKeys   []string            `json:"account_keys"`
	Succeeded     bool                `json:"succeeded"`
	Failure       string              `json:"failure,omitempty"`
	ComputeUnits  uint64              `json:"compute_units"`
	Logs          []string            `json:"logs,omitempty"`
	LogsTruncated bool                `json:"logs_truncated,omitempty"`
	Inner         []InnerInstructions `json:"inner_instructions,omitempty"`
	ReturnData    *ReturnData         `json:"return_data,omitempty"`
}

type RootedSlot struct {
	ParentSlot       uint64         `json:"parent_slot"`
	Blockhash        string         `json:"blockhash"`
	ParentBlockhash  string         `json:"parent_blockhash"`
	Bankhash         string         `json:"bankhash"`
	BlockID          string         `json:"block_id,omitempty"`
	ParentBlockID    string         `json:"parent_block_id,omitempty"`
	FinalitySource   FinalitySource `json:"finality_source"`
	TransactionCount uint32         `json:"transaction_count"`
	AccountCount     uint32         `json:"account_count"`
}

type Event struct {
	SchemaVersion uint32         `json:"schema_version"`
	Cursor        Cursor         `json:"cursor"`
	Kind          string         `json:"kind"`
	Transaction   *Transaction   `json:"transaction,omitempty"`
	Account       *AccountUpdate `json:"account,omitempty"`
	Root          *RootedSlot    `json:"root,omitempty"`
}

type indexedTransaction Transaction

type transactionRecord struct {
	Cursor Cursor `json:"cursor"`
	indexedTransaction
	Version      TransactionVersion `json:"version"`
	SourceSHA256 string             `json:"source_sha256"`
}

type accountRecord struct {
	Cursor       Cursor `json:"cursor"`
	Pubkey       string `json:"pubkey"`
	Owner        string `json:"owner"`
	Lamports     uint64 `json:"lamports"`
	Executable   bool   `json:"executable"`
	RentEpoch    uint64 `json:"rent_epoch"`
	Tombstone    bool   `json:"tombstone"`
	DataSHA256   string `json:"data_sha256"`
	DataBytes    int    `json:"data_bytes"`
	SourceSHA256 string `json:"source_sha256"`
}

type rootRecord struct {
	Cursor Cursor `json:"cursor"`
	RootedSlot
	SourceSHA256 string `json:"source_sha256"`
}

type header struct {
	IndexSchemaVersion uint32           `json:"index_schema_version"`
	EventSchemaVersion uint32           `json:"event_schema_version"`
	Source             SourceDescriptor `json:"source"`
	Filter             Filter           `json:"filter"`
}

type querySnapshot struct {
	Version uint32                `json:"version"`
	Prefix  journal.DurablePrefix `json:"journal_prefix"`
}

type Index struct {
	dir                  string
	store                *journal.Store
	source               SourceDescriptor
	filter               Filter
	startBound           bool
	startAfter           *Cursor
	streamNeedsBatch     bool
	batches              []BatchDescriptor
	activeBatch          *BatchDescriptor
	activeBatchComplete  bool
	seen                 map[Cursor]string
	lastRecordAt         time.Time
	last                 *Cursor
	lastRoot             *Cursor
	lastRootBlockhash    string
	lastRootBlockID      string
	lastRootHasBlockID   bool
	lastTransactionSlot  uint64
	lastTransactionIndex uint32
	haveTransactionIndex bool
	lastAccountSlot      uint64
	lastAccountKey       [32]byte
	haveAccountKey       bool
}

type Status struct {
	SchemaVersion      uint32           `json:"schema_version"`
	EventSchemaVersion uint32           `json:"event_schema_version"`
	Provenance         string           `json:"provenance"`
	Finality           string           `json:"finality"`
	Complete           bool             `json:"complete"`
	Source             SourceDescriptor `json:"source"`
	Filter             Filter           `json:"filter"`
	Start              *StartDescriptor `json:"start,omitempty"`
	Batches            int              `json:"batches"`
	FirstBatch         *BatchDescriptor `json:"first_batch,omitempty"`
	LastBatch          *BatchDescriptor `json:"last_batch,omitempty"`
	Records            int              `json:"records"`
	Transactions       int              `json:"transactions"`
	Accounts           int              `json:"account_updates"`
	Roots              int              `json:"rooted_slots"`
	LastCursor         *Cursor          `json:"last_cursor,omitempty"`
	LastRoot           *Cursor          `json:"last_root,omitempty"`
	LastRecordedAt     *time.Time       `json:"last_recorded_at,omitempty"`
	ChainHead          string           `json:"chain_head_sha256"`
}

type Query struct {
	Owner   string
	Account string
	// After switches results to oldest-unseen-first order so callers can page
	// through bursts without skipping records. A nil cursor keeps the default
	// newest-first snapshot view.
	After       *Cursor
	Limit       int
	IncludeData bool
}

type Result struct {
	Cursor     Cursor `json:"cursor"`
	Pubkey     string `json:"pubkey"`
	Owner      string `json:"owner"`
	Lamports   uint64 `json:"lamports"`
	Executable bool   `json:"executable"`
	RentEpoch  uint64 `json:"rent_epoch"`
	Tombstone  bool   `json:"tombstone"`
	DataSHA256 string `json:"data_sha256"`
	DataBytes  int    `json:"data_bytes"`
	Data       []byte `json:"data,omitempty"`
}

type TransactionQuery struct {
	Signature      string
	Mention        string
	After          *Cursor
	Limit          int
	IncludePayload bool
}

type TransactionResult struct {
	Cursor        Cursor              `json:"cursor"`
	Index         uint32              `json:"index"`
	Signature     string              `json:"signature"`
	Version       TransactionVersion  `json:"version"`
	MessageHash   string              `json:"message_hash"`
	AccountKeys   []string            `json:"account_keys"`
	Succeeded     bool                `json:"succeeded"`
	Failure       string              `json:"failure,omitempty"`
	ComputeUnits  uint64              `json:"compute_units"`
	LogsTruncated bool                `json:"logs_truncated,omitempty"`
	Message       []byte              `json:"message,omitempty"`
	Transaction   []byte              `json:"transaction,omitempty"`
	Logs          []string            `json:"logs,omitempty"`
	Inner         []InnerInstructions `json:"inner_instructions,omitempty"`
	ReturnData    *ReturnData         `json:"return_data,omitempty"`
}

func Open(dir string, source SourceDescriptor, filter Filter) (*Index, error) {
	dir, err := validateDirectory(dir, filter)
	if err != nil {
		return nil, err
	}
	if err := validateSource(source); err != nil {
		return nil, err
	}
	store, err := journal.OpenRotating(filepath.Join(dir, "events.jsonl"))
	if err != nil {
		return nil, err
	}
	index := &Index{dir: dir, store: store, source: source, filter: filter, seen: map[Cursor]string{}}
	records := store.Records()
	if len(records) == 0 {
		record, err := store.Append(time.Now().UTC(), eventHeader, "", header{
			IndexSchemaVersion: IndexSchemaVersion, EventSchemaVersion: EventSchemaVersion,
			Source: source, Filter: filter,
		})
		if err != nil {
			_ = store.Close()
			return nil, err
		}
		index.lastRecordAt = record.At
	} else if err := index.recover(records); err != nil {
		_ = store.Close()
		return nil, err
	} else {
		index.lastRecordAt = records[len(records)-1].At
	}
	if index.queryComplete() {
		if err := index.publishQuerySnapshot(); err != nil {
			_ = store.Close()
			return nil, err
		}
	}
	return index, nil
}

func validateSource(source SourceDescriptor) error {
	if _, err := ProvenanceForCluster(source.Cluster); err != nil {
		return err
	}
	genesis, err := solana.Decode32(source.GenesisHash)
	if err != nil || genesis == ([32]byte{}) {
		return errors.New("rooted source genesis hash is invalid")
	}
	if len(source.AccountsDBRootRunID) != 8 && len(source.AccountsDBRootRunID) != 32 {
		return errors.New("rooted source AccountsDB root run ID is invalid")
	}
	if _, err := hex.DecodeString(source.AccountsDBRootRunID); err != nil ||
		strings.ToLower(source.AccountsDBRootRunID) != source.AccountsDBRootRunID {
		return errors.New("rooted source AccountsDB root run ID is invalid")
	}
	return nil
}

// ProvenanceForCluster returns the exact rooted-evidence label for a source.
func ProvenanceForCluster(cluster string) (string, error) {
	switch cluster {
	case "alpenglow":
		return AlpenglowRootedProvenance, nil
	case "devnet", "testnet", "mainnet-beta":
		return ClassicFinalizedRootedProvenance, nil
	default:
		return "", errors.New("rooted source cluster is unsupported")
	}
}

func validateBatch(batch BatchDescriptor) error {
	if batch.SidecarVersion != SupportedSidecarVersion {
		return fmt.Errorf("rooted batch sidecar version %d is unsupported", batch.SidecarVersion)
	}
	if batch.FromSlot > batch.ThroughSlot {
		return errors.New("rooted batch slot range is invalid")
	}
	if !validSHA256(batch.SHA256) {
		return errors.New("rooted batch hash is invalid")
	}
	return nil
}

func validateDirectory(dir string, filter Filter) (string, error) {
	if dir == "" || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return "", errors.New("index directory must be a clean absolute path")
	}
	if err := validateFilter(filter); err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create index directory: %w", err)
	}
	if err := secureexec.ValidateProtectedDirectory(dir); err != nil {
		return "", errors.New("index directory is not private and trusted")
	}
	if err := os.MkdirAll(filepath.Join(dir, "blobs"), 0o700); err != nil {
		return "", fmt.Errorf("create index blob directory: %w", err)
	}
	if err := secureexec.ValidateProtectedDirectory(filepath.Join(dir, "blobs")); err != nil {
		return "", errors.New("index blob directory is not private and trusted")
	}
	return dir, nil
}

func validateFilter(filter Filter) error {
	if filter.Mention != "" && (filter.Owner != "" || filter.Account != "") {
		return errors.New("mention filter cannot be combined with owner or account")
	}
	if filter.Owner != "" {
		if _, err := solana.Decode32(filter.Owner); err != nil {
			return errors.New("owner filter is not a canonical Solana address")
		}
	}
	if filter.Account != "" {
		if _, err := solana.Decode32(filter.Account); err != nil {
			return errors.New("account filter is not a canonical Solana address")
		}
	}
	if filter.Mention != "" {
		if _, err := solana.Decode32(filter.Mention); err != nil {
			return errors.New("mention filter is not a canonical Solana address")
		}
	}
	return nil
}

func (i *Index) recover(records []journal.Record) error {
	if records[0].Type != eventHeader {
		return errors.New("rooted index has no valid header")
	}
	var stored header
	if err := decodeHeader(records[0].Payload, &stored); err != nil {
		if errors.Is(err, errSourceLessIndex) || errors.Is(err, errV4Index) {
			return err
		}
		return errors.New("rooted index header is invalid")
	}
	if err := validateHeaderVersions(stored); err != nil {
		return err
	}
	if stored.Source != i.source || stored.Filter != i.filter {
		return errors.New("rooted index header does not match the requested source and filter")
	}
	for _, record := range records[1:] {
		switch record.Type {
		case journal.EventRotated:
			continue
		case eventStart:
			if len(i.batches) != 0 || i.last != nil {
				return errors.New("rooted index stream-start record is out of order")
			}
			var payload StartDescriptor
			if err := strictjson.Decode(record.Payload, &payload); err != nil {
				return errors.New("rooted index stream-start record is invalid")
			}
			i.startBound = true
			i.startAfter = cloneCursorPtr(payload.After)
		case eventBatch:
			if !i.startBound {
				return errors.New("rooted index batch has no stream-start binding")
			}
			var payload BatchDescriptor
			if err := strictjson.Decode(record.Payload, &payload); err != nil {
				return errors.New("rooted index batch record is invalid")
			}
			if _, err := i.acceptBatch(payload, false); err != nil {
				return fmt.Errorf("rooted index batch record: %w", err)
			}
		case eventTransaction:
			var payload transactionRecord
			if err := strictjson.Decode(record.Payload, &payload); err != nil {
				return errors.New("rooted index transaction record is invalid")
			}
			version, err := validateTransaction(Transaction(payload.indexedTransaction), i.filter)
			if err != nil {
				return fmt.Errorf("rooted index transaction record: %w", err)
			}
			if payload.Version != version {
				return errors.New("rooted index transaction record has an invalid derived version")
			}
			if err := i.validateTransactionOrder(payload.Cursor, payload.Index); err != nil {
				return err
			}
			if err := i.acceptRecovered(payload.Cursor, payload.SourceSHA256, nil); err != nil {
				return err
			}
			i.lastTransactionSlot, i.lastTransactionIndex, i.haveTransactionIndex =
				payload.Cursor.Slot, payload.Index, true
		case eventAccount:
			var payload accountRecord
			if err := strictjson.Decode(record.Payload, &payload); err != nil {
				return errors.New("rooted index account record is invalid")
			}
			key, err := i.validateAccountRecord(payload)
			if err != nil {
				return err
			}
			if err := i.acceptRecovered(payload.Cursor, payload.SourceSHA256, nil); err != nil {
				return err
			}
			i.lastAccountSlot, i.lastAccountKey, i.haveAccountKey = payload.Cursor.Slot, key, true
		case eventRoot:
			var payload rootRecord
			if err := strictjson.Decode(record.Payload, &payload); err != nil {
				return errors.New("rooted index root record is invalid")
			}
			if err := validateRootRecord(payload, i.source.Cluster); err != nil {
				return err
			}
			root := payload.RootedSlot
			if err := i.acceptRecovered(payload.Cursor, payload.SourceSHA256, &root); err != nil {
				return err
			}
		default:
			return errors.New("rooted index contains an unexpected journal record")
		}
	}
	if len(records) > 1 && !i.startBound {
		return errors.New("rooted index has no stream-start binding")
	}
	return nil
}

func decodeHeader(payload json.RawMessage, target *header) error {
	if err := strictjson.Decode(payload, target); err == nil {
		return nil
	}
	var legacy struct {
		SchemaVersion uint32 `json:"schema_version"`
	}
	if err := json.Unmarshal(payload, &legacy); err == nil && legacy.SchemaVersion > 0 && legacy.SchemaVersion < IndexSchemaVersion {
		return errSourceLessIndex
	}
	return errors.New("invalid rooted index header")
}

func validateHeaderVersions(stored header) error {
	if stored.IndexSchemaVersion == 4 {
		return errV4Index
	}
	if stored.IndexSchemaVersion != IndexSchemaVersion || stored.EventSchemaVersion != EventSchemaVersion {
		return errSourceLessIndex
	}
	return nil
}

func (i *Index) acceptRecovered(cursor Cursor, source string, root *RootedSlot) error {
	if i.activeBatch == nil {
		return errors.New("rooted index event has no batch descriptor")
	}
	if cursor.Slot < i.activeBatch.FromSlot || cursor.Slot > i.activeBatch.ThroughSlot {
		return errors.New("rooted index event is outside its declared batch")
	}
	if i.activeBatchComplete {
		return errors.New("rooted index event follows a completed batch")
	}
	if !validSHA256(source) {
		return errors.New("rooted index contains an invalid source hash")
	}
	isRoot := root != nil
	parent := uint64(0)
	if isRoot {
		parent = root.ParentSlot
	}
	if err := i.validateNext(cursor, isRoot, parent); err != nil {
		return fmt.Errorf("rooted index history: %w", err)
	}
	if isRoot {
		if err := i.validateRootLineage(*root); err != nil {
			return fmt.Errorf("rooted index history: %w", err)
		}
	}
	i.seen[cursor] = source
	i.last = cloneCursor(cursor)
	if isRoot {
		i.rememberRoot(cursor, *root)
		if cursor.Slot == i.activeBatch.ThroughSlot {
			i.activeBatchComplete = true
		}
	}
	return nil
}

// BeginStream binds a framed export to either the beginning of retained
// history or the index's exact durable resume cursor.
func (i *Index) BeginStream(after *Cursor) error {
	start := StartDescriptor{After: cloneCursorPtr(after)}
	if i.startBound {
		expected := i.startAfter
		if i.last != nil {
			expected = i.last
		}
		if !sameCursor(start.After, expected) {
			if len(i.batches) != 0 || i.last != nil {
				return errors.New("rooted stream start does not match the index's recorded start or durable cursor")
			}
			return i.appendStart(start)
		}
		i.streamNeedsBatch = true
		return nil
	}
	if len(i.batches) != 0 || i.last != nil {
		return errors.New("rooted stream start must precede its first batch")
	}
	return i.appendStart(start)
}

func (i *Index) appendStart(start StartDescriptor) error {
	at := time.Now().UTC()
	if at.Before(i.lastRecordAt) {
		at = i.lastRecordAt
	}
	record, err := i.store.Append(at, eventStart, "", start)
	if err != nil {
		return err
	}
	i.lastRecordAt = record.At
	i.startBound = true
	i.startAfter = cloneCursorPtr(start.After)
	i.streamNeedsBatch = true
	return nil
}

// BeginBatch binds following events to one manifest-selected immutable Mithril
// sidecar. Replaying the exact current descriptor is idempotent; all other
// descriptors must extend the prior lineage by one contiguous batch.
func (i *Index) BeginBatch(batch BatchDescriptor) (bool, error) {
	if !i.startBound {
		if err := i.BeginStream(nil); err != nil {
			return false, err
		}
	}
	added, err := i.acceptBatch(batch, true)
	if err == nil {
		i.streamNeedsBatch = false
	}
	return added, err
}

func (i *Index) acceptBatch(batch BatchDescriptor, persist bool) (bool, error) {
	if err := validateBatch(batch); err != nil {
		return false, err
	}
	if len(i.batches) == 0 && i.startBound && i.startAfter != nil && batch.FromSlot <= i.startAfter.Slot {
		return false, errors.New("new rooted index cannot begin from inside a selected batch")
	}
	if i.activeBatch != nil && *i.activeBatch == batch {
		return false, nil
	}
	if i.activeBatch != nil {
		if !i.activeBatchComplete {
			return false, errors.New("rooted batch changed before its terminal root")
		}
		if batch.ManifestSequence != i.activeBatch.ManifestSequence+1 {
			return false, errors.New("rooted batch manifest sequence is not contiguous")
		}
		if batch.FromSlot <= i.activeBatch.ThroughSlot {
			return false, errors.New("rooted batch slot range does not advance")
		}
	}
	if persist {
		at := time.Now().UTC()
		if at.Before(i.lastRecordAt) {
			at = i.lastRecordAt
		}
		record, err := i.store.Append(at, eventBatch, fmt.Sprintf("%d", batch.ManifestSequence), batch)
		if err != nil {
			return false, err
		}
		i.lastRecordAt = record.At
	}
	i.batches = append(i.batches, batch)
	i.activeBatch = &i.batches[len(i.batches)-1]
	i.activeBatchComplete = false
	return true, nil
}

func (i *Index) Append(event Event) (bool, error) {
	return i.append(event, true)
}

func (i *Index) appendBuffered(event Event) (bool, error) {
	return i.append(event, false)
}

func (i *Index) append(event Event, syncRecord bool) (bool, error) {
	if i.activeBatch == nil {
		return false, errors.New("rooted event has no manifest-selected batch descriptor")
	}
	if event.Cursor.Slot < i.activeBatch.FromSlot || event.Cursor.Slot > i.activeBatch.ThroughSlot {
		return false, errors.New("rooted event is outside its declared batch")
	}
	source, transactionVersion, err := validateEvent(event, i.filter, i.source.Cluster)
	if err != nil {
		return false, err
	}
	if previous, exists := i.seen[event.Cursor]; exists {
		if previous == source {
			if syncRecord && event.Kind == "slot_rooted" && i.queryComplete() {
				return false, i.publishQuerySnapshot()
			}
			return false, nil
		}
		return false, errors.New("rooted event cursor was already indexed with different content")
	}
	if i.activeBatchComplete {
		return false, errors.New("rooted event follows a completed batch")
	}
	isRoot := event.Kind == "slot_rooted"
	parent := uint64(0)
	if isRoot {
		parent = event.Root.ParentSlot
	}
	if err := i.validateNext(event.Cursor, isRoot, parent); err != nil {
		return false, err
	}
	if isRoot {
		if err := i.validateRootLineage(*event.Root); err != nil {
			return false, err
		}
	}
	if event.Transaction != nil {
		if err := i.validateTransactionOrder(event.Cursor, event.Transaction.Index); err != nil {
			return false, err
		}
	}
	var accountKey [32]byte
	if event.Account != nil {
		accountKey, _ = solana.Decode32(event.Account.Pubkey)
		if i.haveAccountKey && i.lastAccountSlot == event.Cursor.Slot && bytes.Compare(i.lastAccountKey[:], accountKey[:]) >= 0 {
			return false, errors.New("rooted account updates are not in strictly ascending address order")
		}
	}

	var payload any
	switch {
	case event.Transaction != nil:
		payload = transactionRecord{
			Cursor: event.Cursor, indexedTransaction: indexedTransaction(*event.Transaction),
			Version: transactionVersion, SourceSHA256: source,
		}
	case event.Account != nil:
		dataHash := sha256.Sum256(event.Account.Data)
		dataSHA := hex.EncodeToString(dataHash[:])
		if len(event.Account.Data) > 0 {
			if err := i.storeBlob(dataSHA, event.Account.Data); err != nil {
				return false, err
			}
		}
		payload = accountRecord{
			Cursor: event.Cursor, Pubkey: event.Account.Pubkey, Owner: event.Account.Owner,
			Lamports: event.Account.Lamports, Executable: event.Account.Executable,
			RentEpoch: event.Account.RentEpoch, Tombstone: event.Account.Tombstone,
			DataSHA256: dataSHA, DataBytes: len(event.Account.Data), SourceSHA256: source,
		}
	default:
		payload = rootRecord{
			Cursor: event.Cursor, RootedSlot: *event.Root, SourceSHA256: source,
		}
	}
	at := time.Now().UTC()
	if at.Before(i.lastRecordAt) {
		at = i.lastRecordAt
	}
	typeName := eventAccount
	if event.Transaction != nil {
		typeName = eventTransaction
	} else if isRoot {
		typeName = eventRoot
	}
	var appendErr error
	if syncRecord {
		_, appendErr = i.store.Append(at, typeName, event.Cursor.String(), payload)
	} else {
		_, appendErr = i.store.AppendBuffered(at, typeName, event.Cursor.String(), payload)
	}
	if appendErr != nil {
		return false, appendErr
	}
	i.lastRecordAt = at
	i.seen[event.Cursor] = source
	i.last = cloneCursor(event.Cursor)
	if event.Transaction != nil {
		i.lastTransactionSlot, i.lastTransactionIndex, i.haveTransactionIndex =
			event.Cursor.Slot, event.Transaction.Index, true
	} else if event.Account != nil {
		i.lastAccountSlot, i.lastAccountKey, i.haveAccountKey = event.Cursor.Slot, accountKey, true
	}
	if isRoot {
		i.rememberRoot(event.Cursor, *event.Root)
		if event.Cursor.Slot == i.activeBatch.ThroughSlot {
			i.activeBatchComplete = true
		}
	}
	if syncRecord && i.queryComplete() {
		if err := i.publishQuerySnapshot(); err != nil {
			return true, err
		}
	}
	return true, nil
}

func (i *Index) queryComplete() bool {
	return i.last != nil && i.lastRoot != nil && *i.last == *i.lastRoot && i.activeBatchComplete
}

func (i *Index) syncDurableView() error {
	if i.queryComplete() {
		return i.publishQuerySnapshot()
	}
	return i.store.Sync()
}

func (i *Index) publishQuerySnapshot() error {
	prefix, err := i.store.DurablePrefix()
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(querySnapshot{Version: querySnapshotVersion, Prefix: prefix})
	if err != nil {
		return errors.New("encode rooted index query snapshot")
	}
	if err := securefile.ReplacePrivate(
		filepath.Join(i.dir, querySnapshotName), append(encoded, '\n'), maxQuerySnapshotBytes,
	); err != nil {
		return fmt.Errorf("publish rooted index query snapshot: %w", err)
	}
	return nil
}

func validateEvent(event Event, filter Filter, cluster string) (string, TransactionVersion, error) {
	if event.SchemaVersion != SchemaVersion {
		return "", "", errors.New("rooted event schema version is unsupported")
	}
	var version TransactionVersion
	switch event.Kind {
	case "transaction_executed":
		if event.Transaction == nil || event.Account != nil || event.Root != nil {
			return "", "", errors.New("transaction event must contain only transaction data")
		}
		var err error
		version, err = validateTransaction(*event.Transaction, filter)
		if err != nil {
			return "", "", err
		}
	case "account_updated":
		if event.Account == nil || event.Transaction != nil || event.Root != nil {
			return "", "", errors.New("account event must contain only account data")
		}
		if _, err := solana.Decode32(event.Account.Pubkey); err != nil {
			return "", "", errors.New("rooted event account address is invalid")
		}
		if _, err := solana.Decode32(event.Account.Owner); err != nil {
			return "", "", errors.New("rooted event owner address is invalid")
		}
		if len(event.Account.Data) > maxAccountDataBytes {
			return "", "", errors.New("rooted event account data exceeds Solana's 10 MiB limit")
		}
		if event.Account.Tombstone != (event.Account.Lamports == 0) {
			return "", "", errors.New("rooted event tombstone does not match its lamport balance")
		}
		if filter.Owner != "" && event.Account.Owner != filter.Owner ||
			filter.Account != "" && event.Account.Pubkey != filter.Account {
			return "", "", errors.New("rooted event does not match the index filter")
		}
		if filter.Mention != "" {
			return "", "", errors.New("account event does not match the transaction mention filter")
		}
	case "slot_rooted":
		if event.Root == nil || event.Transaction != nil || event.Account != nil {
			return "", "", errors.New("root event must contain only complete root data")
		}
		if err := validateRootedSlot(event.Cursor, *event.Root, cluster); err != nil {
			return "", "", err
		}
	default:
		return "", "", errors.New("rooted event kind is unsupported")
	}
	canonical, err := json.Marshal(event)
	if err != nil {
		return "", "", errors.New("encode rooted event")
	}
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:]), version, nil
}

func validateTransaction(transaction Transaction, filter Filter) (TransactionVersion, error) {
	if _, err := solana.Decode64(transaction.Signature); err != nil {
		return "", errors.New("rooted transaction signature is invalid")
	}
	if len(transaction.Transaction) == 0 || len(transaction.Transaction) > solanago.MaxTransactionSizeV1 {
		return "", fmt.Errorf("rooted transaction wire size is outside allowed range 1..%d", solanago.MaxTransactionSizeV1)
	}
	decoded, err := solanago.TransactionFromBytes(transaction.Transaction)
	if err != nil {
		return "", errors.New("rooted transaction wire is invalid")
	}
	canonical, err := decoded.MarshalBinary()
	if err != nil || !bytes.Equal(canonical, transaction.Transaction) {
		return "", errors.New("rooted transaction wire is not canonical")
	}
	if err := decoded.Sanitize(); err != nil {
		return "", errors.New("rooted transaction wire failed structural validation")
	}
	if err := decoded.VerifySignatures(); err != nil {
		return "", errors.New("rooted transaction signature verification failed")
	}
	version, err := transactionVersion(decoded.Message.GetVersion())
	if err != nil {
		return "", err
	}
	if version != TransactionVersionV1 && len(transaction.Transaction) > maxLegacyTransactionBytes {
		return "", fmt.Errorf("rooted transaction legacy/v0 wire exceeds %d bytes", maxLegacyTransactionBytes)
	}
	if len(decoded.Signatures) == 0 || decoded.Signatures[0].String() != transaction.Signature {
		return "", errors.New("rooted transaction signature does not match wire")
	}
	messageHash, err := transactionMessageHash(decoded)
	if err != nil || solanago.Hash(messageHash).String() != transaction.MessageHash {
		return "", errors.New("rooted transaction message hash does not match wire")
	}
	if len(transaction.AccountKeys) == 0 || len(transaction.AccountKeys) > 256 {
		return "", errors.New("rooted transaction account count is invalid")
	}
	mentioned := filter.Mention == ""
	for _, value := range transaction.AccountKeys {
		if _, err := solana.Decode32(value); err != nil {
			return "", errors.New("rooted transaction account address is invalid")
		}
		mentioned = mentioned || value == filter.Mention
	}
	wantAccounts := len(decoded.Message.AccountKeys)
	if version == TransactionVersionV0 {
		wantAccounts += decoded.Message.GetAddressTableLookups().NumLookups()
	}
	if len(transaction.AccountKeys) != wantAccounts {
		return "", errors.New("rooted transaction account keys do not match wire")
	}
	for index, key := range decoded.Message.AccountKeys {
		if transaction.AccountKeys[index] != key.String() {
			return "", errors.New("rooted transaction static account does not match wire")
		}
	}
	if filter.Owner != "" || filter.Account != "" || !mentioned {
		return "", errors.New("rooted transaction does not match the index filter")
	}
	if transaction.Succeeded == (transaction.Failure != "") {
		return "", errors.New("rooted transaction result is inconsistent")
	}
	logBytes := 0
	for _, log := range transaction.Logs {
		logBytes += len(log)
		if logBytes > maxLogBytes {
			return "", errors.New("rooted transaction logs exceed the bounded recorder limit")
		}
	}
	if transaction.LogsTruncated &&
		(len(transaction.Logs) == 0 || transaction.Logs[len(transaction.Logs)-1] != "Log truncated") {
		return "", errors.New("rooted transaction truncated-log marker is invalid")
	}
	innerCount := 0
	var lastGroup uint8
	for groupIndex, group := range transaction.Inner {
		if groupIndex > 0 && group.Index <= lastGroup {
			return "", errors.New("rooted transaction inner-instruction groups are not ordered")
		}
		lastGroup = group.Index
		for _, instruction := range group.Instructions {
			innerCount++
			if innerCount > 64 || int(instruction.ProgramIDIndex) >= len(transaction.AccountKeys) ||
				len(instruction.Accounts) > 255 || len(instruction.Data) > 10<<10 {
				return "", errors.New("rooted transaction inner instruction exceeds runtime bounds")
			}
			for _, account := range instruction.Accounts {
				if int(account) >= len(transaction.AccountKeys) {
					return "", errors.New("rooted transaction inner-instruction account index is invalid")
				}
			}
		}
	}
	if transaction.ReturnData != nil {
		if _, err := solana.Decode32(transaction.ReturnData.ProgramID); err != nil ||
			len(transaction.ReturnData.Data) > maxReturnDataBytes {
			return "", errors.New("rooted transaction return data is invalid")
		}
	}
	return version, nil
}

func transactionVersion(version solanago.MessageVersion) (TransactionVersion, error) {
	switch version {
	case solanago.MessageVersionLegacy:
		return TransactionVersionLegacy, nil
	case solanago.MessageVersionV0:
		return TransactionVersionV0, nil
	case solanago.MessageVersionV1:
		return TransactionVersionV1, nil
	default:
		return "", errors.New("rooted transaction version is unsupported")
	}
}

func transactionMessageHash(transaction *solanago.Transaction) ([32]byte, error) {
	message, err := transaction.Message.MarshalBinary()
	if err != nil {
		return [32]byte{}, err
	}
	hasher := blake3.New()
	_, _ = hasher.Write([]byte("solana-tx-message-v1"))
	_, _ = hasher.Write(message)
	var hash [32]byte
	hasher.Sum(hash[:0])
	return hash, nil
}

func (i *Index) validateAccountRecord(payload accountRecord) ([32]byte, error) {
	key, err := solana.Decode32(payload.Pubkey)
	if err != nil {
		return [32]byte{}, errors.New("rooted index contains an invalid account address")
	}
	if _, err := solana.Decode32(payload.Owner); err != nil {
		return [32]byte{}, errors.New("rooted index contains an invalid owner address")
	}
	if payload.DataBytes < 0 || payload.DataBytes > maxAccountDataBytes || !validSHA256(payload.DataSHA256) ||
		payload.Tombstone != (payload.Lamports == 0) {
		return [32]byte{}, errors.New("rooted index contains invalid account metadata")
	}
	if i.filter.Owner != "" && payload.Owner != i.filter.Owner ||
		i.filter.Account != "" && payload.Pubkey != i.filter.Account {
		return [32]byte{}, errors.New("rooted index account record does not match its bound filter")
	}
	if i.haveAccountKey && i.lastAccountSlot == payload.Cursor.Slot && bytes.Compare(i.lastAccountKey[:], key[:]) >= 0 {
		return [32]byte{}, errors.New("rooted index account records are not in strictly ascending address order")
	}
	if payload.DataBytes > 0 {
		if _, err := readBlob(i.dir, payload); err != nil {
			return [32]byte{}, err
		}
	}
	return key, nil
}

func validateRootRecord(payload rootRecord, cluster string) error {
	if err := validateRootedSlot(payload.Cursor, payload.RootedSlot, cluster); err != nil {
		return fmt.Errorf("rooted index root record: %w", err)
	}
	return nil
}

func validateRootedSlot(cursor Cursor, root RootedSlot, cluster string) error {
	if uint64(root.TransactionCount)+uint64(root.AccountCount) != uint64(cursor.Ordinal) {
		return errors.New("root event cursor does not match its transaction and account counts")
	}
	if cursor.Slot == 0 && root.ParentSlot != 0 || cursor.Slot > 0 && root.ParentSlot >= cursor.Slot {
		return errors.New("root event parent slot is invalid")
	}
	if value, err := solana.Decode32(root.Bankhash); err != nil || value == ([32]byte{}) {
		return errors.New("root event bankhash is invalid")
	}
	if value, err := solana.Decode32(root.Blockhash); err != nil || value == ([32]byte{}) {
		return errors.New("root event blockhash is invalid")
	}
	if value, err := solana.Decode32(root.ParentBlockhash); err != nil || cursor.Slot > 0 && value == ([32]byte{}) {
		return errors.New("root event parent blockhash is invalid")
	}
	hasBlockID := root.BlockID != "" || root.ParentBlockID != ""
	if hasBlockID {
		if root.BlockID == "" || root.ParentBlockID == "" {
			return errors.New("root event has incomplete Alpenglow block identity")
		}
		if value, err := solana.Decode32(root.BlockID); err != nil || value == ([32]byte{}) {
			return errors.New("root event Alpenglow block ID is invalid")
		}
		if value, err := solana.Decode32(root.ParentBlockID); err != nil || value == ([32]byte{}) {
			return errors.New("root event Alpenglow parent block ID is invalid")
		}
	}
	switch root.FinalitySource {
	case FinalityAlpenglowCertificate, FinalityAlpenglowDelegated:
		if !hasBlockID {
			return errors.New("root event Alpenglow finality has no block identity")
		}
	case FinalityRPCFinalized:
		if hasBlockID {
			return errors.New("root event RPC finality unexpectedly carries Alpenglow block identity")
		}
	default:
		return errors.New("root event finality source is invalid")
	}
	switch cluster {
	case "alpenglow":
		if root.FinalitySource != FinalityAlpenglowCertificate &&
			root.FinalitySource != FinalityAlpenglowDelegated {
			return errors.New("root event finality source does not match the bound Alpenglow cluster")
		}
	case "devnet", "testnet", "mainnet-beta":
		if root.FinalitySource != FinalityRPCFinalized {
			return errors.New("root event finality source does not match the bound classic cluster")
		}
	default:
		return errors.New("root event cluster is unsupported")
	}
	return nil
}

func (i *Index) validateRootLineage(root RootedSlot) error {
	if i.lastRoot == nil {
		return nil
	}
	if root.ParentBlockhash != i.lastRootBlockhash {
		return errors.New("rooted slot parent blockhash does not match the previous rooted slot")
	}
	hasBlockID := root.BlockID != ""
	if hasBlockID != i.lastRootHasBlockID || hasBlockID && root.ParentBlockID != i.lastRootBlockID {
		return errors.New("rooted slot parent block ID does not match the previous rooted slot")
	}
	return nil
}

func (i *Index) rememberRoot(cursor Cursor, root RootedSlot) {
	i.lastRoot = cloneCursor(cursor)
	i.lastRootBlockhash = root.Blockhash
	i.lastRootBlockID = root.BlockID
	i.lastRootHasBlockID = root.BlockID != ""
}

func (i *Index) validateNext(cursor Cursor, root bool, parent uint64) error {
	if root && i.lastRoot == nil && i.startAfter != nil && cursor.Slot > i.startAfter.Slot &&
		parent != i.startAfter.Slot {
		return errors.New("first rooted slot parent does not match the stream start")
	}
	if i.last == nil {
		return nil
	}
	if cursor.Slot < i.last.Slot || cursor.Slot == i.last.Slot && cursor.Ordinal <= i.last.Ordinal {
		return errors.New("rooted event cursor did not advance")
	}
	if cursor.Slot == i.last.Slot {
		if i.lastRoot != nil && i.lastRoot.Slot == cursor.Slot {
			return errors.New("rooted event appeared after the slot's root marker")
		}
		if root && i.lastRoot != nil && parent != i.lastRoot.Slot {
			return errors.New("rooted slot parent does not match the previous rooted slot")
		}
		return nil
	}
	if i.lastRoot == nil || i.lastRoot.Slot != i.last.Slot {
		return errors.New("rooted event advanced slots before the prior slot root marker")
	}
	if root && parent != i.lastRoot.Slot {
		return errors.New("rooted slot parent does not match the previous rooted slot")
	}
	return nil
}

func (i *Index) validateTransactionOrder(cursor Cursor, index uint32) error {
	if cursor.Ordinal != index {
		return errors.New("rooted transaction cursor does not match its block index")
	}
	if i.haveAccountKey && i.lastAccountSlot == cursor.Slot {
		return errors.New("rooted transaction appeared after an account update")
	}
	if i.haveTransactionIndex && i.lastTransactionSlot == cursor.Slot &&
		index <= i.lastTransactionIndex {
		return errors.New("rooted transaction indexes did not advance")
	}
	return nil
}

func (i *Index) storeBlob(hash string, data []byte) error {
	path := filepath.Join(i.dir, "blobs", hash+".bin")
	if err := securefile.CreatePrivate(path, data, maxAccountDataBytes); err == nil {
		return nil
	}
	existing, err := securefile.ReadPrivate(path, maxAccountDataBytes)
	if err != nil || !bytes.Equal(existing, data) {
		return errors.New("account data blob already exists with different or unreadable content")
	}
	return nil
}

func (i *Index) Close() error { return i.store.Close() }

// HasEvents reports whether this index already contains at least one rooted
// event. The header alone does not make an empty first ingest successful.
func (i *Index) HasEvents() bool { return i.last != nil }

// ReadPreamble consumes the source and stream-start records and returns the
// same buffered reader so no bytes read ahead from a pipe are lost.
func ReadPreamble(input io.Reader) (SourceDescriptor, StartDescriptor, *bufio.Reader, error) {
	if input == nil {
		return SourceDescriptor{}, StartDescriptor{}, nil, errors.New("rooted source input is required")
	}
	reader, ok := input.(*bufio.Reader)
	if !ok {
		reader = bufio.NewReaderSize(input, 16<<10)
	}
	sourceLine, err := readPreambleLine(reader, "source")
	if err != nil {
		return SourceDescriptor{}, StartDescriptor{}, nil, err
	}
	var source sourceFrame
	if err := strictjson.Decode(sourceLine, &source); err != nil || source.RecordType != SourceRecordType {
		return SourceDescriptor{}, StartDescriptor{}, nil, errors.New("Mithril rooted stream has no valid source frame")
	}
	if err := validateSource(source.Source); err != nil {
		return SourceDescriptor{}, StartDescriptor{}, nil, err
	}
	startLine, err := readPreambleLine(reader, "start")
	if err != nil {
		return SourceDescriptor{}, StartDescriptor{}, nil, err
	}
	var start startFrame
	if err := strictjson.Decode(startLine, &start); err != nil || start.RecordType != StartRecordType || start.Start == nil {
		return SourceDescriptor{}, StartDescriptor{}, nil, errors.New("Mithril rooted stream has no valid start frame")
	}
	return source.Source, *start.Start, reader, nil
}

func readPreambleLine(reader *bufio.Reader, name string) ([]byte, error) {
	for {
		line, err := reader.ReadSlice('\n')
		if errors.Is(err, bufio.ErrBufferFull) {
			return nil, fmt.Errorf("rooted %s frame exceeds 16 KiB input limit", name)
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return nil, fmt.Errorf("read rooted %s frame: %w", name, err)
		}
		line = bytes.TrimSpace(line)
		if len(line) > 0 {
			return line, nil
		}
		if errors.Is(err, io.EOF) {
			if name == "source" {
				return nil, errors.New("Mithril rooted stream is empty or ended before its source frame")
			}
			return nil, fmt.Errorf("Mithril rooted stream ended before its %s frame", name)
		}
	}
}

func Ingest(ctx context.Context, index *Index, input io.Reader) (int, error) {
	return IngestWithProgress(ctx, index, input, nil)
}

// IngestWithProgress ingests the same durable stream and reports only the
// number of newly stored records. The callback never receives event contents.
func IngestWithProgress(ctx context.Context, index *Index, input io.Reader, progress func(int)) (int, error) {
	if index == nil || input == nil {
		return 0, errors.New("index and input are required")
	}
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 64<<10), maxEventBytes)
	stored := 0
	unsynced := 0
	wantStart := false
	wantBatch := index.streamNeedsBatch
	allowsEmptyPage := func() bool {
		if index.last != nil {
			return index.activeBatchComplete
		}
		return index.startAfter != nil && len(index.batches) == 0
	}
	finish := func(ingestErr error) (int, error) {
		return stored, errors.Join(ingestErr, index.syncDurableView())
	}
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return finish(err)
		}
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var envelope struct {
			RecordType string `json:"record_type"`
		}
		if err := json.Unmarshal(line, &envelope); err != nil {
			return finish(fmt.Errorf("decode rooted stream record: %w", err))
		}
		if envelope.RecordType != "" {
			switch envelope.RecordType {
			case BatchRecordType:
				if wantStart {
					return finish(errors.New("repeated Mithril rooted source has no following start frame"))
				}
				var frame batchFrame
				if err := strictjson.Decode(line, &frame); err != nil {
					return finish(fmt.Errorf("decode rooted batch frame: %w", err))
				}
				if _, err := index.BeginBatch(frame.Batch); err != nil {
					return finish(fmt.Errorf("index rooted batch %d: %w", frame.Batch.ManifestSequence, err))
				}
				wantBatch = false
				continue
			case SourceRecordType:
				if wantBatch {
					if !allowsEmptyPage() {
						return finish(errors.New("Mithril rooted start has no following batch frame"))
					}
					wantBatch = false
				}
				if wantStart {
					return finish(errors.New("repeated Mithril rooted source has no following start frame"))
				}
				var frame sourceFrame
				if err := strictjson.Decode(line, &frame); err != nil {
					return finish(fmt.Errorf("decode repeated rooted source frame: %w", err))
				}
				if err := validateSource(frame.Source); err != nil || frame.Source != index.source {
					return finish(errors.New("repeated Mithril rooted source does not match the bound index source"))
				}
				wantStart = true
				continue
			case StartRecordType:
				if wantBatch || !wantStart {
					return finish(errors.New("Mithril rooted start frame has no preceding source frame"))
				}
				var frame startFrame
				if err := strictjson.Decode(line, &frame); err != nil {
					return finish(fmt.Errorf("decode rooted start frame: %w", err))
				}
				if frame.Start == nil {
					return finish(errors.New("Mithril rooted stream has no valid start frame"))
				}
				if err := index.BeginStream(frame.Start.After); err != nil {
					return finish(err)
				}
				wantStart = false
				wantBatch = true
				continue
			default:
				return finish(errors.New("rooted stream record type is unsupported"))
			}
		}
		if wantStart {
			return finish(errors.New("repeated Mithril rooted source has no following start frame"))
		}
		if wantBatch {
			return finish(errors.New("Mithril rooted start has no following batch frame"))
		}
		var event Event
		if err := strictjson.Decode(line, &event); err != nil {
			return finish(fmt.Errorf("decode rooted event: %w", err))
		}
		added, err := index.appendBuffered(event)
		if err != nil {
			return finish(fmt.Errorf("index rooted event %s: %w", event.Cursor, err))
		}
		if added {
			stored++
			unsynced++
			if progress != nil {
				progress(stored)
			}
		}
		if unsynced > 0 && (event.Kind == "slot_rooted" || unsynced >= 1024) {
			if err := index.syncDurableView(); err != nil {
				return stored, err
			}
			unsynced = 0
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) || strings.Contains(err.Error(), "token too long") {
			return finish(errors.New("rooted event exceeds 16 MiB input limit"))
		}
		return finish(fmt.Errorf("read rooted events: %w", err))
	}
	if wantStart {
		return finish(errors.New("repeated Mithril rooted source has no following start frame"))
	}
	if wantBatch && !allowsEmptyPage() {
		return finish(errors.New("Mithril rooted start has no following batch frame"))
	}
	if index.activeBatch != nil && !index.activeBatchComplete {
		return finish(fmt.Errorf("rooted batch %d is incomplete: terminal root for slot %d was not received",
			index.activeBatch.ManifestSequence, index.activeBatch.ThroughSlot))
	}
	if index.last != nil {
		return finish(requireCompleteCursors(index.last, index.lastRoot))
	}
	return finish(nil)
}

func ReadStatus(dir string) (Status, error) {
	records, err := readRecords(dir)
	if err != nil {
		return Status{}, err
	}
	status, _, _, err := parseRecords(dir, records)
	if err != nil {
		return Status{}, err
	}
	if len(records) > 0 {
		status.ChainHead = records[len(records)-1].Hash
		recordedAt := records[len(records)-1].At.UTC()
		status.LastRecordedAt = &recordedAt
	}
	return status, nil
}

// ReadCompleteStatus verifies that the durable prefix ends at a slot root.
// ReadStatus remains available so an interrupted ingest can report its cursor.
func ReadCompleteStatus(dir string) (Status, error) {
	status, err := ReadStatus(dir)
	if err != nil {
		return Status{}, err
	}
	return status, requireComplete(status)
}

func QueryAccounts(dir string, query Query) ([]Result, error) {
	if query.Limit <= 0 || query.Limit > 10_000 {
		return nil, errors.New("query limit must be between 1 and 10000")
	}
	if query.Owner != "" {
		if _, err := solana.Decode32(query.Owner); err != nil {
			return nil, errors.New("query owner is not a canonical Solana address")
		}
	}
	if query.Account != "" {
		if _, err := solana.Decode32(query.Account); err != nil {
			return nil, errors.New("query account is not a canonical Solana address")
		}
	}
	records, err := readRecords(dir)
	if err != nil {
		return nil, err
	}
	status, accounts, _, err := parseRecords(dir, records)
	if err != nil {
		return nil, err
	}
	if err := requireComplete(status); err != nil {
		return nil, err
	}
	// ponytail: this linear scan is intentionally the first bounded proof. Move
	// to an embedded KV index if retained history makes measured query latency
	// unacceptable; the durable journal remains the recovery source of truth.
	results := make([]Result, 0, min(query.Limit, len(accounts)))
	start, end, step := 0, len(accounts), 1
	if query.After != nil {
		start, end, step = len(accounts)-1, -1, -1
	}
	for index := start; index != end; index += step {
		account := accounts[index]
		if query.Owner != "" && account.Owner != query.Owner ||
			query.Account != "" && account.Pubkey != query.Account ||
			query.After != nil && !cursorAfter(account.Cursor, *query.After) {
			continue
		}
		result := Result{
			Cursor: account.Cursor, Pubkey: account.Pubkey, Owner: account.Owner,
			Lamports: account.Lamports, Executable: account.Executable,
			RentEpoch: account.RentEpoch, Tombstone: account.Tombstone,
			DataSHA256: account.DataSHA256, DataBytes: account.DataBytes,
		}
		if query.IncludeData && account.DataBytes > 0 {
			data, err := readBlob(dir, account)
			if err != nil {
				return nil, err
			}
			result.Data = data
		}
		results = append(results, result)
		if len(results) == query.Limit {
			break
		}
	}
	return results, nil
}

func QueryTransactions(dir string, query TransactionQuery) ([]TransactionResult, error) {
	if query.Limit <= 0 || query.Limit > 10_000 {
		return nil, errors.New("query limit must be between 1 and 10000")
	}
	if query.Signature != "" {
		if _, err := solana.Decode64(query.Signature); err != nil {
			return nil, errors.New("query signature is not canonical")
		}
	}
	if query.Mention != "" {
		if _, err := solana.Decode32(query.Mention); err != nil {
			return nil, errors.New("query mention is not a canonical Solana address")
		}
	}
	records, err := readRecords(dir)
	if err != nil {
		return nil, err
	}
	status, _, transactions, err := parseRecords(dir, records)
	if err != nil {
		return nil, err
	}
	if err := requireComplete(status); err != nil {
		return nil, err
	}
	results := make([]TransactionResult, 0, min(query.Limit, len(transactions)))
	start, end, step := 0, len(transactions), 1
	if query.After != nil {
		start, end, step = len(transactions)-1, -1, -1
	}
	for index := start; index != end; index += step {
		transaction := transactions[index]
		if query.Signature != "" && transaction.Signature != query.Signature ||
			query.Mention != "" && !contains(transaction.AccountKeys, query.Mention) ||
			query.After != nil && !cursorAfter(transaction.Cursor, *query.After) {
			continue
		}
		result := TransactionResult{
			Cursor: transaction.Cursor, Index: transaction.Index,
			Signature: transaction.Signature, Version: transaction.Version,
			MessageHash: transaction.MessageHash,
			AccountKeys: append([]string(nil), transaction.AccountKeys...),
			Succeeded:   transaction.Succeeded, Failure: transaction.Failure,
			ComputeUnits: transaction.ComputeUnits, LogsTruncated: transaction.LogsTruncated,
		}
		if query.IncludePayload {
			decoded, err := solanago.TransactionFromBytes(transaction.Transaction)
			if err != nil {
				return nil, errors.New("rooted index transaction wire is invalid")
			}
			message, err := decoded.Message.MarshalBinary()
			if err != nil {
				return nil, errors.New("rooted index transaction message is invalid")
			}
			result.Message = message
			result.Transaction = append([]byte(nil), transaction.Transaction...)
			result.Logs = append([]string(nil), transaction.Logs...)
			result.Inner = cloneInner(transaction.Inner)
			result.ReturnData = cloneReturnData(transaction.ReturnData)
		}
		results = append(results, result)
		if len(results) == query.Limit {
			break
		}
	}
	return results, nil
}

func readRecords(dir string) ([]journal.Record, error) {
	if dir == "" || !filepath.IsAbs(dir) || filepath.Clean(dir) != dir {
		return nil, errors.New("index directory must be a clean absolute path")
	}
	if err := secureexec.ValidateProtectedDirectory(dir); err != nil {
		return nil, errors.New("index directory is not private and trusted")
	}
	journalPath := filepath.Join(dir, "events.jsonl")
	records, err := journal.ReadRecords(journalPath)
	if err == nil || !errors.Is(err, journal.ErrLocked) {
		return records, err
	}
	return readQuerySnapshot(dir, journalPath)
}

func readQuerySnapshot(dir, journalPath string) ([]journal.Record, error) {
	path := filepath.Join(dir, querySnapshotName)
	var last error
	for range 3 {
		encoded, err := securefile.ReadPrivate(path, maxQuerySnapshotBytes)
		if err != nil {
			if errors.Is(err, securefile.ErrChanged) {
				last = err
				continue
			}
			if errors.Is(err, os.ErrNotExist) {
				return nil, errors.New("rooted index has no complete query snapshot yet")
			}
			return nil, fmt.Errorf("read rooted index query snapshot: %w", err)
		}
		var snapshot querySnapshot
		if err := strictjson.Decode(encoded, &snapshot); err != nil || snapshot.Version != querySnapshotVersion {
			return nil, errors.New("rooted index query snapshot is invalid")
		}
		records, err := journal.ReadDurablePrefix(journalPath, snapshot.Prefix)
		if errors.Is(err, journal.ErrPrefixChanged) {
			last = err
			continue
		}
		if err != nil {
			return nil, err
		}
		return records, nil
	}
	return nil, fmt.Errorf("rooted index query snapshot changed repeatedly: %w", last)
}

func parseRecords(dir string, records []journal.Record) (Status, []accountRecord, []transactionRecord, error) {
	if len(records) == 0 || records[0].Type != eventHeader {
		return Status{}, nil, nil, errors.New("rooted index has no valid header")
	}
	var stored header
	if err := decodeHeader(records[0].Payload, &stored); err != nil {
		if errors.Is(err, errSourceLessIndex) || errors.Is(err, errV4Index) {
			return Status{}, nil, nil, err
		}
		return Status{}, nil, nil, errors.New("rooted index header is invalid")
	}
	if err := validateHeaderVersions(stored); err != nil {
		return Status{}, nil, nil, err
	}
	if err := validateFilter(stored.Filter); err != nil {
		return Status{}, nil, nil, errors.New("rooted index header filter is invalid")
	}
	if err := validateSource(stored.Source); err != nil {
		return Status{}, nil, nil, errors.New("rooted index header source is invalid")
	}
	validator := &Index{dir: dir, source: stored.Source, filter: stored.Filter, seen: map[Cursor]string{}}
	if err := validator.recover(records); err != nil {
		return Status{}, nil, nil, err
	}
	provenance, err := ProvenanceForCluster(stored.Source.Cluster)
	if err != nil {
		return Status{}, nil, nil, errors.New("rooted index header source is invalid")
	}
	status := Status{
		SchemaVersion: IndexSchemaVersion, EventSchemaVersion: EventSchemaVersion, Provenance: provenance,
		Finality: RootedFinality, Source: stored.Source, Filter: stored.Filter, Records: len(records),
	}
	accounts := make([]accountRecord, 0)
	transactions := make([]transactionRecord, 0)
	for _, record := range records[1:] {
		switch record.Type {
		case journal.EventRotated:
			continue
		case eventStart:
			var payload StartDescriptor
			if err := strictjson.Decode(record.Payload, &payload); err != nil {
				return Status{}, nil, nil, errors.New("rooted index stream-start record is invalid")
			}
			copy := payload
			copy.After = cloneCursorPtr(payload.After)
			status.Start = &copy
		case eventBatch:
			var payload BatchDescriptor
			if err := strictjson.Decode(record.Payload, &payload); err != nil {
				return Status{}, nil, nil, errors.New("rooted index batch record is invalid")
			}
			status.Batches++
			if status.FirstBatch == nil {
				copy := payload
				status.FirstBatch = &copy
			}
			copy := payload
			status.LastBatch = &copy
		case eventTransaction:
			var payload transactionRecord
			if err := strictjson.Decode(record.Payload, &payload); err != nil {
				return Status{}, nil, nil, errors.New("rooted index transaction record is invalid")
			}
			transactions = append(transactions, payload)
			status.Transactions++
			status.LastCursor = cloneCursor(payload.Cursor)
		case eventAccount:
			var payload accountRecord
			if err := strictjson.Decode(record.Payload, &payload); err != nil {
				return Status{}, nil, nil, errors.New("rooted index account record is invalid")
			}
			accounts = append(accounts, payload)
			status.Accounts++
			status.LastCursor = cloneCursor(payload.Cursor)
		case eventRoot:
			var payload rootRecord
			if err := strictjson.Decode(record.Payload, &payload); err != nil {
				return Status{}, nil, nil, errors.New("rooted index root record is invalid")
			}
			status.Roots++
			status.LastCursor = cloneCursor(payload.Cursor)
			status.LastRoot = cloneCursor(payload.Cursor)
		default:
			return Status{}, nil, nil, errors.New("rooted index contains an unexpected journal record")
		}
	}
	sort.SliceStable(accounts, func(a, b int) bool { return cursorAfter(accounts[a].Cursor, accounts[b].Cursor) })
	sort.SliceStable(transactions, func(a, b int) bool {
		return cursorAfter(transactions[a].Cursor, transactions[b].Cursor)
	})
	status.Complete = status.LastCursor != nil && status.LastRoot != nil &&
		status.LastCursor.Slot == status.LastRoot.Slot && validator.activeBatchComplete
	return status, accounts, transactions, nil
}

func requireComplete(status Status) error {
	if !status.Complete && status.LastBatch != nil && status.LastCursor != nil && status.LastRoot != nil &&
		status.LastCursor.Slot == status.LastRoot.Slot {
		return fmt.Errorf("rooted index is incomplete: batch %d has not reached its terminal root at slot %d",
			status.LastBatch.ManifestSequence, status.LastBatch.ThroughSlot)
	}
	return requireCompleteCursors(status.LastCursor, status.LastRoot)
}

func requireCompleteCursors(last, root *Cursor) error {
	if last == nil {
		return errors.New("rooted index is incomplete: no rooted slot has been stored")
	}
	if root != nil && last.Slot == root.Slot {
		return nil
	}
	return fmt.Errorf("rooted index is incomplete at %s; resume ingest after that cursor until its slot root marker", last)
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func cloneInner(value []InnerInstructions) []InnerInstructions {
	result := make([]InnerInstructions, len(value))
	for i, group := range value {
		result[i].Index = group.Index
		result[i].Instructions = make([]CompiledInstruction, len(group.Instructions))
		for j, instruction := range group.Instructions {
			result[i].Instructions[j] = CompiledInstruction{
				ProgramIDIndex: instruction.ProgramIDIndex,
				Accounts:       append([]uint16(nil), instruction.Accounts...),
				Data:           append([]byte(nil), instruction.Data...),
			}
		}
	}
	return result
}

func cloneReturnData(value *ReturnData) *ReturnData {
	if value == nil {
		return nil
	}
	return &ReturnData{ProgramID: value.ProgramID, Data: append([]byte(nil), value.Data...)}
}

func readBlob(dir string, account accountRecord) ([]byte, error) {
	if !validSHA256(account.DataSHA256) || account.DataBytes <= 0 || account.DataBytes > maxAccountDataBytes {
		return nil, errors.New("rooted index account blob metadata is invalid")
	}
	data, err := securefile.ReadPrivate(filepath.Join(dir, "blobs", account.DataSHA256+".bin"), maxAccountDataBytes)
	if err != nil || len(data) != account.DataBytes {
		return nil, errors.New("rooted index account blob is missing or invalid")
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != account.DataSHA256 {
		return nil, errors.New("rooted index account blob hash does not match")
	}
	return data, nil
}

func ParseCursor(value string) (Cursor, error) {
	var cursor Cursor
	if _, err := fmt.Sscanf(value, "%d:%d", &cursor.Slot, &cursor.Ordinal); err != nil || cursor.String() != value {
		return Cursor{}, errors.New("cursor must use SLOT:ORDINAL with canonical decimal numbers")
	}
	return cursor, nil
}

func cursorAfter(value, after Cursor) bool {
	return value.Slot > after.Slot || value.Slot == after.Slot && value.Ordinal > after.Ordinal
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 || strings.ToLower(value) != value {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func cloneCursor(cursor Cursor) *Cursor {
	copy := cursor
	return &copy
}

func cloneCursorPtr(cursor *Cursor) *Cursor {
	if cursor == nil {
		return nil
	}
	return cloneCursor(*cursor)
}

func sameCursor(left, right *Cursor) bool {
	return left == nil && right == nil || left != nil && right != nil && *left == *right
}
