package mcpobserve

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os/exec"
	"sort"
	"strconv"
	"time"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/internal/mcpstdio"
	"github.com/Overclock-Validator/mithril-agent/internal/secureexec"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/solana"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	accountTool     = "mithril_get_account_info"
	diagnosisTool   = "mithril_diagnose"
	genesisTool     = "mithril_get_genesis_hash"
	infoTool        = "mithril_mcp_info"
	stateTool       = "mithril_read_shutdown_state"
	maxCatalogPages = 8
	maxCatalogTools = 256
	maxCatalogBytes = 1 << 20
	maxResultBytes  = 1 << 20
	// One inbound frame carries a tool result plus its JSON-RPC envelope.
	maxFrameBytes      = 2 << 20
	maxDiagnosisChecks = 64
)

var systemProgram = solana.Encode(make([]byte, 32))

type Config struct {
	Command   string
	Args      []string
	Env       []string
	Cluster   string
	RPCOrigin string
}

// observeWaitDelay bounds how long Run may wait on pipes after the child is
// killed.
const observeWaitDelay = 5 * time.Second

type Client struct {
	config Config
	now    func() time.Time
}

type stageError struct {
	stage string
	err   error
}

func (e *stageError) Error() string { return e.err.Error() }
func (e *stageError) Unwrap() error { return e.err }

// FailureStage returns a bounded phase name without exposing the underlying
// MCP error to operator surfaces.
func FailureStage(err error) string {
	var staged *stageError
	if errors.As(err, &staged) {
		return staged.stage
	}
	return ""
}

func failAt(stage string, err error) error {
	return &stageError{stage: stage, err: err}
}

func New(config Config, now func() time.Time) (*Client, error) {
	if config.Cluster != "devnet" {
		return nil, errors.New("MCP observation is restricted to devnet")
	}
	if config.RPCOrigin == "" {
		return nil, errors.New("expected Mithril RPC origin is required")
	}
	if err := secureexec.ValidateExecutable(config.Command); err != nil {
		return nil, err
	}
	if err := secureexec.ValidateEnvironment(config.Env); err != nil {
		return nil, fmt.Errorf("MCP %w", err)
	}
	if now == nil {
		now = time.Now
	}
	config.Args = append([]string(nil), config.Args...)
	config.Env = append([]string(nil), config.Env...)
	return &Client{config: config, now: now}, nil
}

func (c *Client) Observe(ctx context.Context, source string) (agent.NodeObservation, error) {
	if _, err := solana.Decode32(source); err != nil {
		return agent.NodeObservation{}, fmt.Errorf("source: %w", err)
	}
	if err := secureexec.ValidateExecutable(c.config.Command); err != nil {
		return agent.NodeObservation{}, err
	}
	command := exec.CommandContext(ctx, c.config.Command, c.config.Args...)
	// Cancelling the context kills the child, but Run does not return until the
	// output pipes close — and a grandchild can hold them open indefinitely,
	// blocking the runner's only goroutine with nothing to detect the hang.
	command.WaitDelay = observeWaitDelay
	command.Env = secureexec.MCPEnvironment(c.config.Env)
	stderr := &secureexec.DiscardCounter{}
	command.Stderr = stderr

	// The SDK's own command transport decodes each message with an unbounded
	// json.Decoder, so a compromised or faulty server could buffer one enormous
	// value until the agent runs out of memory. Drive the pipes directly so
	// every inbound frame is bounded before it is decoded.
	childInput, err := command.StdinPipe()
	if err != nil {
		return agent.NodeObservation{}, errors.New("open Mithril MCP input")
	}
	childOutput, err := command.StdoutPipe()
	if err != nil {
		return agent.NodeObservation{}, errors.New("open Mithril MCP output")
	}
	if err := command.Start(); err != nil {
		return agent.NodeObservation{}, errors.New("start Mithril MCP")
	}
	// Reap the child on every path, including ones that return before the
	// session is closed.
	stopChild := func() {
		_ = childInput.Close()
		_ = childOutput.Close()
		if command.Process != nil {
			_ = command.Process.Kill()
		}
		_ = command.Wait()
	}
	defer stopChild()

	client := mcpsdk.NewClient(&mcpsdk.Implementation{
		Name:    "mithril-agent",
		Version: "0.1.0-dev",
	}, &mcpsdk.ClientOptions{Capabilities: &mcpsdk.ClientCapabilities{}})
	session, err := client.Connect(ctx, &mcpsdk.IOTransport{
		Reader: mcpstdio.NewReaderSize(childOutput, maxFrameBytes),
		Writer: mcpstdio.WriteCloser{Writer: childInput},
	}, nil)
	if err != nil {
		return agent.NodeObservation{}, errors.New("connect to Mithril MCP")
	}
	closeSession := func() error {
		if err := session.Close(); err != nil {
			return errors.New("close Mithril MCP session")
		}
		return nil
	}
	initialize := session.InitializeResult()
	if initialize == nil || initialize.ServerInfo == nil ||
		initialize.ServerInfo.Name != "mithril" || initialize.ServerInfo.Version == "" {
		_ = closeSession()
		return agent.NodeObservation{}, errors.New("connected server is not Mithril MCP")
	}

	if err := requireTools(ctx, session); err != nil {
		_ = closeSession()
		return agent.NodeObservation{}, failAt("catalog", err)
	}
	infoResult, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: infoTool})
	if err != nil {
		_ = closeSession()
		return agent.NodeObservation{}, failAt("info", errors.New("call Mithril MCP info tool"))
	}
	if infoResult.IsError {
		_ = closeSession()
		return agent.NodeObservation{}, failAt("info", errors.New("Mithril MCP info tool returned an error"))
	}
	if err := verifyRPCOrigin(infoResult.StructuredContent, c.config.RPCOrigin); err != nil {
		_ = closeSession()
		return agent.NodeObservation{}, failAt("info", err)
	}
	genesisResult, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: genesisTool})
	if err != nil {
		_ = closeSession()
		return agent.NodeObservation{}, failAt("genesis", errors.New("call Mithril genesis tool"))
	}
	if genesisResult.IsError {
		_ = closeSession()
		return agent.NodeObservation{}, failAt("genesis", errors.New("Mithril genesis tool returned an error"))
	}
	if err := verifyGenesisHash(genesisResult.StructuredContent); err != nil {
		_ = closeSession()
		return agent.NodeObservation{}, failAt("genesis", err)
	}
	stateResult, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: stateTool})
	if err != nil {
		_ = closeSession()
		return agent.NodeObservation{}, failAt("state_call", errors.New("call Mithril state tool"))
	}
	if stateResult.IsError {
		_ = closeSession()
		return agent.NodeObservation{}, failAt("state_tool", errors.New("Mithril state tool returned an error"))
	}
	if err := verifyNodeIdentity(stateResult.StructuredContent); err != nil {
		_ = closeSession()
		return agent.NodeObservation{}, failAt("state_identity", err)
	}
	// The node service writes to journald. Automation uses the required
	// structured checks instead of depending on an optional file log.
	diagnosisResult, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: diagnosisTool,
		Arguments: map[string]any{
			"include_logs":         false,
			"include_replay_trend": false,
		},
	})
	if err != nil {
		_ = closeSession()
		return agent.NodeObservation{}, failAt("diagnosis", errors.New("call Mithril diagnosis tool"))
	}
	if diagnosisResult.IsError {
		_ = closeSession()
		return agent.NodeObservation{}, failAt("diagnosis", errors.New("Mithril diagnosis tool returned an error"))
	}
	health, err := parseDiagnosisResult(diagnosisResult.StructuredContent, c.now().UTC())
	if err != nil {
		_ = closeSession()
		return agent.NodeObservation{}, failAt("diagnosis", err)
	}
	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{
		Name: accountTool,
		Arguments: map[string]any{
			"pubkey":      source,
			"encoding":    "base64",
			"data_length": uint64(0),
		},
	})
	if err != nil {
		_ = closeSession()
		return agent.NodeObservation{}, failAt("account", errors.New("call Mithril account tool"))
	}
	if result.IsError {
		_ = closeSession()
		return agent.NodeObservation{}, failAt("account", errors.New("Mithril account tool returned an error"))
	}
	observation, err := parseAccountResult(result.StructuredContent, c.config.Cluster, source, c.now().UTC())
	if closeErr := closeSession(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		return agent.NodeObservation{}, failAt("account", err)
	}
	return agent.NodeObservation{Account: observation, Health: health}, nil
}

func (c *Client) ObserveAccount(ctx context.Context, source string) (agent.Observation, error) {
	observation, err := c.Observe(ctx, source)
	return observation.Account, err
}

func requireTools(ctx context.Context, session *mcpsdk.ClientSession) error {
	cursor := ""
	seenCursors := map[string]bool{}
	found := map[string]int{
		accountTool:   0,
		diagnosisTool: 0,
		genesisTool:   0,
		infoTool:      0,
		stateTool:     0,
	}
	total := 0
	totalBytes := 0
	for page := 0; page < maxCatalogPages; page++ {
		params := &mcpsdk.ListToolsParams{Cursor: cursor}
		if cursor == "" {
			params = nil
		}
		result, err := session.ListTools(ctx, params)
		if err != nil {
			return errors.New("list Mithril MCP tools")
		}
		total += len(result.Tools)
		if total > maxCatalogTools {
			return errors.New("Mithril MCP tool catalog exceeds limit")
		}
		for _, tool := range result.Tools {
			if tool != nil {
				encoded, err := json.Marshal(tool)
				if err != nil {
					return errors.New("encode Mithril MCP tool metadata")
				}
				totalBytes += len(encoded)
				if totalBytes > maxCatalogBytes {
					return errors.New("Mithril MCP tool catalog exceeds byte limit")
				}
				if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
					return fmt.Errorf("Mithril tool %s is not read-only", tool.Name)
				}
				if _, required := found[tool.Name]; !required {
					continue
				}
				found[tool.Name]++
			}
		}
		if result.NextCursor == "" {
			break
		}
		if seenCursors[result.NextCursor] {
			return errors.New("Mithril MCP tool catalog repeated a cursor")
		}
		seenCursors[result.NextCursor] = true
		cursor = result.NextCursor
		if page == maxCatalogPages-1 {
			return errors.New("Mithril MCP tool catalog exceeds page limit")
		}
	}
	for name, count := range found {
		if count != 1 {
			return fmt.Errorf("Mithril tool %s count is %d, want 1", name, count)
		}
	}
	return nil
}

type nodeStateResult struct {
	Found                bool            `json:"found"`
	State                json.RawMessage `json:"state,omitempty"`
	ParsedShutdownReason string          `json:"parsed_shutdown_reason,omitempty"`
	IsErrorShutdown      bool            `json:"is_error_shutdown"`
}

type genesisHashResult struct {
	GenesisHash string `json:"genesis_hash"`
}

type infoResult struct {
	RPCConfigured bool   `json:"rpc_configured"`
	RPCOrigin     string `json:"rpc_origin"`
}

type diagnosisResult struct {
	Status            string `json:"status"`
	AssessmentScope   string `json:"assessment_scope"`
	ObservedAt        string `json:"observed_at"`
	SafeForAutomation *bool  `json:"safe_for_automation"`
	EvidenceComplete  *bool  `json:"evidence_complete"`
	Checks            []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"checks"`
	DivergenceArtifacts []json.RawMessage     `json:"divergence_artifacts"`
	SlotsBehind         *agent.SlotComparison `json:"slots_behind"`
}

func parseDiagnosisResult(value any, now time.Time) (agent.NodeHealth, error) {
	if value == nil {
		return agent.NodeHealth{}, errors.New("Mithril diagnosis omitted structured content")
	}
	encoded, err := boundedJSON(value, maxResultBytes)
	if err != nil {
		return agent.NodeHealth{}, errors.New("encode Mithril diagnosis result")
	}
	var result diagnosisResult
	if err := json.Unmarshal(encoded, &result); err != nil {
		return agent.NodeHealth{}, errors.New("decode Mithril diagnosis result")
	}
	if result.AssessmentScope != "point_in_time_snapshot" ||
		result.SafeForAutomation == nil || *result.SafeForAutomation ||
		result.EvidenceComplete == nil {
		return agent.NodeHealth{}, errors.New("Mithril diagnosis contract is unsupported")
	}
	switch result.Status {
	case "healthy", "degraded", "critical", "unknown":
	default:
		return agent.NodeHealth{}, errors.New("Mithril diagnosis status is invalid")
	}
	observedAt, err := time.Parse(time.RFC3339Nano, result.ObservedAt)
	if err != nil {
		return agent.NodeHealth{}, errors.New("Mithril diagnosis time is invalid")
	}
	now = now.UTC()
	observedAt = observedAt.UTC()
	if observedAt.After(now.Add(5*time.Second)) || now.Sub(observedAt) > 2*time.Minute {
		return agent.NodeHealth{}, errors.New("Mithril diagnosis time is outside the freshness window")
	}
	if len(result.Checks) > maxDiagnosisChecks {
		return agent.NodeHealth{}, errors.New("Mithril diagnosis has too many checks")
	}
	seen := make(map[string]bool, len(result.Checks))
	issues := make([]agent.HealthIssue, 0, len(result.Checks))
	computedStatus := "healthy"
	for _, check := range result.Checks {
		if check.Name == "" || seen[check.Name] {
			return agent.NodeHealth{}, errors.New("Mithril diagnosis check set is invalid")
		}
		seen[check.Name] = true
		switch check.Status {
		case "ok", "skipped":
		case "degraded":
			issues = append(issues, agent.HealthIssue{Name: check.Name, Status: check.Status})
			if computedStatus == "healthy" {
				computedStatus = "degraded"
			}
		case "unknown":
			issues = append(issues, agent.HealthIssue{Name: check.Name, Status: check.Status})
			if computedStatus != "critical" {
				computedStatus = "unknown"
			}
		case "critical":
			issues = append(issues, agent.HealthIssue{Name: check.Name, Status: check.Status})
			computedStatus = "critical"
		default:
			return agent.NodeHealth{}, errors.New("Mithril diagnosis check status is invalid")
		}
	}
	if result.Status != computedStatus {
		return agent.NodeHealth{}, errors.New("Mithril diagnosis status disagrees with its checks")
	}
	if err := validateSlotComparison(result.SlotsBehind); err != nil {
		return agent.NodeHealth{}, err
	}
	sort.Slice(issues, func(i, j int) bool { return issues[i].Name < issues[j].Name })
	for _, required := range []string{
		"metrics",
		"runtime_provenance",
		"verification",
		"block_source",
		"rpc",
		"state",
		"state_evidence",
		"shutdown",
		"runtime_state_agreement",
		"divergence_artifacts",
		"host",
	} {
		if !seen[required] {
			return agent.NodeHealth{}, fmt.Errorf("Mithril diagnosis omitted required check %s", required)
		}
	}
	return agent.NodeHealth{
		Status:              result.Status,
		AssessmentScope:     result.AssessmentScope,
		ObservedAt:          observedAt,
		SafeForAutomation:   *result.SafeForAutomation,
		EvidenceComplete:    *result.EvidenceComplete,
		DivergenceArtifacts: len(result.DivergenceArtifacts),
		Issues:              issues,
		CrossCheck:          result.SlotsBehind,
	}, nil
}

func validateSlotComparison(comparison *agent.SlotComparison) error {
	if comparison == nil || comparison.ReferenceCommitment != "confirmed" ||
		comparison.MithrilView != "local_unfinalized_head" || comparison.Threshold == 0 ||
		comparison.Threshold > 1_000 {
		return errors.New("Mithril slot comparison contract is invalid")
	}
	wantStatus := "in_sync"
	var wantBehind int64
	if comparison.ReferenceSlot >= comparison.MithrilSlot {
		delta := comparison.ReferenceSlot - comparison.MithrilSlot
		if delta > math.MaxInt64 {
			return errors.New("Mithril slot comparison exceeds supported range")
		}
		wantBehind = int64(delta)
		if delta > comparison.Threshold {
			wantStatus = "behind"
		}
	} else {
		delta := comparison.MithrilSlot - comparison.ReferenceSlot
		if delta > math.MaxInt64 {
			return errors.New("Mithril slot comparison exceeds supported range")
		}
		wantBehind = -int64(delta)
		wantStatus = "ahead"
	}
	if comparison.SlotsBehind != wantBehind || comparison.Status != wantStatus {
		return errors.New("Mithril slot comparison is inconsistent")
	}
	return nil
}

func verifyGenesisHash(value any) error {
	if value == nil {
		return errors.New("Mithril genesis tool omitted structured content")
	}
	encoded, err := boundedJSON(value, maxResultBytes)
	if err != nil {
		return errors.New("encode Mithril genesis result")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var result genesisHashResult
	if err := decoder.Decode(&result); err != nil {
		return errors.New("decode Mithril genesis result")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("Mithril genesis result contains multiple values")
	}
	if result.GenesisHash != solana.DevnetGenesisHash {
		return errors.New("Mithril RPC endpoint is not devnet")
	}
	return nil
}

func verifyRPCOrigin(value any, expected string) error {
	if value == nil {
		return errors.New("Mithril MCP info tool omitted structured content")
	}
	encoded, err := boundedJSON(value, maxResultBytes)
	if err != nil {
		return errors.New("encode Mithril MCP info result")
	}
	var result infoResult
	if err := json.Unmarshal(encoded, &result); err != nil {
		return errors.New("decode Mithril MCP info result")
	}
	if !result.RPCConfigured || result.RPCOrigin != expected {
		return errors.New("Mithril MCP and transaction RPC origins differ")
	}
	return nil
}

func verifyNodeIdentity(value any) error {
	if value == nil {
		return errors.New("Mithril state tool omitted structured content")
	}
	encoded, err := boundedJSON(value, maxResultBytes)
	if err != nil {
		return errors.New("encode Mithril state result")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var result nodeStateResult
	if err := decoder.Decode(&result); err != nil {
		return errors.New("decode Mithril state result")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("Mithril state result contains multiple values")
	}
	if !result.Found || len(result.State) == 0 {
		return errors.New("Mithril state is unavailable")
	}
	if result.IsErrorShutdown {
		return errors.New("Mithril state reports an error shutdown")
	}
	if err := strictjson.Validate(result.State); err != nil {
		return errors.New("Mithril state identity is ambiguous")
	}
	var state map[string]json.RawMessage
	if err := json.Unmarshal(result.State, &state); err != nil {
		return errors.New("decode Mithril state identity")
	}
	schemaSupported, err := stateBool(state, "schema_supported")
	if err != nil || !schemaSupported {
		return errors.New("Mithril state schema is unsupported")
	}
	cluster, err := stateString(state, "cluster")
	if err != nil || cluster != "devnet" {
		return errors.New("Mithril state cluster is not devnet")
	}
	genesis, err := stateString(state, "genesis_hash")
	if err != nil || genesis != solana.DevnetGenesisHash {
		return errors.New("Mithril state genesis hash is not devnet")
	}
	stage, err := stateString(state, "stage")
	if err != nil || stage != "ready" {
		return errors.New("Mithril state is not ready")
	}
	if raw, ok := state["source_clock_anomaly"]; ok {
		var anomaly bool
		if err := json.Unmarshal(raw, &anomaly); err != nil || anomaly {
			return errors.New("Mithril state clock evidence is invalid")
		}
	}
	if raw, ok := state["corruption_reason"]; ok {
		var reason string
		if err := json.Unmarshal(raw, &reason); err != nil || reason != "" {
			return errors.New("Mithril state reports corruption")
		}
	}
	return nil
}

func stateBool(state map[string]json.RawMessage, name string) (bool, error) {
	raw, ok := state[name]
	if !ok {
		return false, errors.New("state field is missing")
	}
	var value bool
	if err := json.Unmarshal(raw, &value); err != nil {
		return false, err
	}
	return value, nil
}

func stateString(state map[string]json.RawMessage, name string) (string, error) {
	raw, ok := state[name]
	if !ok {
		return "", errors.New("state field is missing")
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || value == "" {
		return "", errors.New("state field is invalid")
	}
	return value, nil
}

type accountResult struct {
	Found   bool `json:"found"`
	Context struct {
		APIVersion string `json:"apiVersion,omitempty"`
		Slot       uint64 `json:"slot"`
	} `json:"context"`
	Value *struct {
		Data          [2]string `json:"data"`
		Executable    bool      `json:"executable"`
		Lamports      string    `json:"lamports"`
		Owner         string    `json:"owner"`
		RentEpoch     string    `json:"rent_epoch"`
		Space         uint64    `json:"space"`
		DataOffset    uint64    `json:"data_offset"`
		DataLength    uint64    `json:"data_length"`
		DataTruncated bool      `json:"data_truncated"`
	} `json:"value"`
	Finality    string `json:"finality"`
	Consistency string `json:"consistency"`
}

func parseAccountResult(value any, cluster, source string, observedAt time.Time) (agent.Observation, error) {
	if value == nil {
		return agent.Observation{}, errors.New("Mithril account tool omitted structured content")
	}
	encoded, err := boundedJSON(value, maxResultBytes)
	if err != nil {
		return agent.Observation{}, errors.New("encode Mithril account result")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var result accountResult
	if err := decoder.Decode(&result); err != nil {
		return agent.Observation{}, errors.New("decode Mithril account result")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return agent.Observation{}, errors.New("Mithril account result contains multiple values")
	}
	if !result.Found || result.Value == nil {
		return agent.Observation{}, errors.New("source account was not found by Mithril")
	}
	if result.Context.Slot == 0 {
		return agent.Observation{}, errors.New("Mithril account result has no slot")
	}
	if result.Finality != "local_unfinalized" || result.Consistency != "node_reported_non_atomic" {
		return agent.Observation{}, errors.New("Mithril account result has unexpected evidence semantics")
	}
	account := result.Value
	if account.Executable || account.Owner != systemProgram || account.Space != 0 ||
		account.DataOffset != 0 || account.DataLength != 0 || account.DataTruncated ||
		account.Data != [2]string{"", "base64"} {
		return agent.Observation{}, errors.New("source is not a plain system account")
	}
	balance, err := strconv.ParseUint(account.Lamports, 10, 64)
	if err != nil {
		return agent.Observation{}, errors.New("Mithril account balance is invalid")
	}
	return agent.Observation{
		Cluster:         cluster,
		Source:          source,
		BalanceLamports: balance,
		Slot:            result.Context.Slot,
		ObservedAt:      observedAt.UTC(),
		EvidenceSource:  "mithril_mcp",
		Finality:        result.Finality,
		Consistency:     result.Consistency,
	}, nil
}

func boundedJSON(value any, limit int) ([]byte, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if len(encoded) > limit {
		return nil, errors.New("structured content exceeds size limit")
	}
	return encoded, nil
}
