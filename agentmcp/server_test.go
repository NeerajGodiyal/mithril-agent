package agentmcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/execution"
	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/internal/operatorstatus"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type providerStub struct{}

func (providerStub) Info() (Info, error) {
	return Info{
		Version: Version, Profile: "orca_devnet_swap_v1", ProfileVersion: 1,
		Cluster: "devnet", Action: "orca_devnet_swap_v1",
		Execution: "bounded_devnet_only", TradingImplemented: true,
	}, nil
}

func (providerStub) OperatorGuide() OperatorGuide {
	return localOperatorGuide()
}

// The stub returns a fully populated settings block so the disclosure test has
// something real to assert the absence of an address against.
func (providerStub) Strategy() (StrategySettings, error) {
	return StrategySettings{
		Configured: true, Direction: "sell SOL for devUSDC",
		InputPerAction: "0.005000000 SOL", DailyCap: "0.048600000 SOL",
		MaxFee: "0.000100000 SOL", FundedTradesPerDay: 6,
		PriceRule:       "sell_at_or_above $250.000000",
		ControlMode:     "stopped",
		SweepConfigured: true, SweepProofValid: true,
		SweepKeepBehind: "0.127500000 SOL", SweepMaxPerSend: "1.000000000 SOL",
		SweepDailyCap: "2.000000000 SOL",
	}, nil
}

type buyProviderStub struct{ providerStub }

func (buyProviderStub) Info() (Info, error) {
	return Info{
		Version: Version, Profile: "orca_devnet_buy_v2", ProfileVersion: 2,
		Cluster: "devnet", Action: "orca_devnet_buy_v2",
		Execution: "bounded_devnet_only", TradingImplemented: true,
	}, nil
}

func (buyProviderStub) Status() (operatorstatus.View, error) {
	return operatorstatus.View{
		Profile: "orca_devnet_buy_v2", ProfileVersion: 2, Cluster: "devnet",
		RunnerState: "recent", Control: control.Status{Mode: control.ModeNoNewActions},
		Result: execution.Result{Decision: "stopped"},
		LastAction: operatorstatus.Action{
			ObservedAt: time.Date(2026, time.August, 2, 10, 0, 0, 0, time.UTC),
			Result: execution.Result{
				ActionID: "buy-action", Decision: "complete", Verdict: "finalized",
				InputAmount: 100_000, InputAsset: "devUSDC",
				MinimumOutput: 1_000_000, OutputAmount: 1_010_000, OutputAsset: "SOL",
			},
		},
	}, nil
}

func localOperatorGuide() OperatorGuide {
	return OperatorGuide{
		SafeLocalCommand: "mithril-agent demo --config PATH",
		CapabilityBoundaries: []string{
			"This MCP server exposes status and guidance only; it has no action or control tool.",
			"The demonstration command must be run locally by an operator with a protected configuration path.",
			"These MCP tools and Telegram commands expose no authority to authorize, enable, sign, or submit a transaction.",
			"Do not grant an assistant shell access to the protected configuration or demonstration command.",
		},
	}
}

func (providerStub) Status() (operatorstatus.View, error) {
	return operatorstatus.View{
		Profile: "orca_devnet_swap_v1", ProfileVersion: 1, Cluster: "devnet",
		RunnerState: "not_started", Control: control.Status{Mode: control.ModeNoNewActions},
		Result: execution.Result{Decision: "stopped", PriceTrigger: &pricetrigger.Status{
			Feed: pricetrigger.FeedSOLUSD, Direction: pricetrigger.SellAtOrAbove,
			ThresholdMicros: 75_000_000, Available: true,
			ConservativePrice: 74_000_000, ObservedAt: time.Date(2026, time.August, 1, 10, 0, 0, 0, time.UTC),
			PrimaryPublishedAt:   time.Date(2026, time.August, 1, 9, 59, 59, 0, time.UTC),
			SecondaryPublishedAt: time.Date(2026, time.August, 1, 9, 59, 59, 0, time.UTC),
		}},
		LastAction: operatorstatus.Action{
			ObservedAt: time.Date(2026, time.August, 1, 10, 0, 0, 0, time.UTC),
			Result:     execution.Result{ActionID: "action-1", Decision: "complete"},
		},
	}, nil
}

func TestServerWorksWithAStandardMCPClient(t *testing.T) {
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- Serve(ctx, providerStub{}, serverReader, serverWriter)
	}()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{
		Name: "interoperability-test", Version: "1",
	}, nil)
	session, err := client.Connect(ctx, &mcpsdk.IOTransport{
		Reader: clientReader, Writer: clientWriter,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	initialized := session.InitializeResult()
	if initialized == nil || initialized.ProtocolVersion == "" ||
		initialized.ServerInfo == nil || initialized.ServerInfo.Name != "mithril-agent" ||
		initialized.ServerInfo.Title != "Mithril autonomous operations agent" ||
		initialized.ServerInfo.Version != Version || initialized.Capabilities == nil ||
		initialized.Capabilities.Tools == nil || initialized.Instructions == "" {
		t.Fatalf("initialize result = %+v", initialized)
	}
	listed, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 4 {
		t.Fatalf("tool count = %d", len(listed.Tools))
	}
	wantTools := map[string]bool{
		"mithril_agent_info":           true,
		"mithril_agent_status":         true,
		"mithril_agent_strategy":       true,
		"mithril_agent_operator_guide": true,
	}
	for _, tool := range listed.Tools {
		if !wantTools[tool.Name] {
			t.Fatalf("unexpected tool %q", tool.Name)
		}
		delete(wantTools, tool.Name)
		if tool.Annotations == nil || !tool.Annotations.ReadOnlyHint ||
			!tool.Annotations.IdempotentHint || tool.Annotations.OpenWorldHint == nil ||
			*tool.Annotations.OpenWorldHint {
			t.Fatalf("tool %q is not marked read-only, idempotent, and closed-domain", tool.Name)
		}
	}
	if len(wantTools) != 0 {
		t.Fatalf("missing tools: %+v", wantTools)
	}
	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "mithril_agent_info"})
	if err != nil || result.IsError {
		t.Fatalf("info call = %+v, %v", result, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var info Info
	if err := json.Unmarshal(encoded, &info); err != nil {
		t.Fatal(err)
	}
	if !info.TradingImplemented || info.TelegramHasAuthority || info.MainnetEnabled {
		t.Fatalf("info = %+v", info)
	}
	result, err = session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "mithril_agent_operator_guide"})
	if err != nil || result.IsError {
		t.Fatalf("operator guide call = %+v, %v", result, err)
	}
	encoded, err = json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var guide OperatorGuide
	if err := json.Unmarshal(encoded, &guide); err != nil {
		t.Fatal(err)
	}
	wantBoundary := []string{
		"This MCP server exposes status and guidance only; it has no action or control tool.",
		"The demonstration command must be run locally by an operator with a protected configuration path.",
		"These MCP tools and Telegram commands expose no authority to authorize, enable, sign, or submit a transaction.",
		"Do not grant an assistant shell access to the protected configuration or demonstration command.",
	}
	if guide.SafeLocalCommand != "mithril-agent demo --config PATH" ||
		len(guide.CapabilityBoundaries) != len(wantBoundary) {
		t.Fatalf("operator guide = %+v", guide)
	}
	for index := range wantBoundary {
		if guide.CapabilityBoundaries[index] != wantBoundary[index] {
			t.Fatalf("operator guide boundary %d = %q", index, guide.CapabilityBoundaries[index])
		}
	}
	result, err = session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "mithril_agent_status"})
	if err != nil || result.IsError {
		t.Fatalf("status call = %+v, %v", result, err)
	}
	encoded, err = json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var status operatorstatus.View
	if err := json.Unmarshal(encoded, &status); err != nil {
		t.Fatal(err)
	}
	if status.Profile != "orca_devnet_swap_v1" || status.Cluster != "devnet" ||
		status.Control.Mode != control.ModeNoNewActions ||
		status.LastAction.Result.ActionID != "action-1" || status.Result.PriceTrigger == nil ||
		status.Result.PriceTrigger.ThresholdMicros != 75_000_000 ||
		status.Result.PriceTrigger.ConditionMet {
		t.Fatalf("status = %+v", status)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	cancel()
	_ = clientWriter.Close()
	_ = clientReader.Close()
	_ = serverReader.Close()
	_ = serverWriter.Close()
	select {
	case err := <-serverErrors:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("MCP server did not stop after disconnect")
	}
}

func TestServerRejectsToolsBeforeInitialize(t *testing.T) {
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- Serve(ctx, providerStub{}, serverReader, serverWriter)
	}()

	if _, err := fmt.Fprintln(clientWriter, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(clientReader).ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(line, &response); err != nil {
		t.Fatal(err)
	}
	if response.Error == nil || !strings.Contains(response.Error.Message, "session initialization") {
		t.Fatalf("response error = %+v", response.Error)
	}

	_ = clientWriter.Close()
	_ = clientReader.Close()
	select {
	case err := <-serverErrors:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("MCP server did not stop after input closed")
	}
}

func TestBuyStatusRoundTripsThroughAStandardMCPClient(t *testing.T) {
	serverReader, clientWriter := io.Pipe()
	clientReader, serverWriter := io.Pipe()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	serverErrors := make(chan error, 1)
	go func() {
		serverErrors <- Serve(ctx, buyProviderStub{}, serverReader, serverWriter)
	}()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{
		Name: "buy-interoperability-test", Version: "1",
	}, nil)
	session, err := client.Connect(ctx, &mcpsdk.IOTransport{
		Reader: clientReader, Writer: clientWriter,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "mithril_agent_status"})
	if err != nil || result.IsError {
		t.Fatalf("buy status call = %+v, %v", result, err)
	}
	encoded, err := json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var status operatorstatus.View
	if err := json.Unmarshal(encoded, &status); err != nil {
		t.Fatal(err)
	}
	if status.Profile != "orca_devnet_buy_v2" || status.ProfileVersion != 2 ||
		status.LastAction.Result.InputAsset != "devUSDC" ||
		status.LastAction.Result.OutputAsset != "SOL" ||
		status.LastAction.Result.InputAmount != 100_000 ||
		status.LastAction.Result.OutputAmount != 1_010_000 {
		t.Fatalf("buy MCP status = %+v", status)
	}
	if err := session.Close(); err != nil {
		t.Fatal(err)
	}
	cancel()
	_ = clientWriter.Close()
	_ = clientReader.Close()
	_ = serverReader.Close()
	_ = serverWriter.Close()
	select {
	case err := <-serverErrors:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("MCP server did not stop after buy client disconnect")
	}
}

// The settings surface answers "what am I configured to do" for an assistant an
// operator is talking to. It must not become a way to read the wallet: Info
// promises to describe the agent "without exposing endpoints, accounts, or
// credentials", and a sweep destination is an account.
//
// Whether a destination is configured and whether its proof still verifies
// answers every operational question without naming it.
func TestStrategySettingsNeverCarryAnAddress(t *testing.T) {
	settings, err := providerStub{}.Strategy()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(settings)
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)

	// A base58 Solana address is 32-44 chars of the base58 alphabet. Anything
	// that long and that shaped in this payload is an account leaking out.
	address := regexp.MustCompile(`[1-9A-HJ-NP-Za-km-z]{32,44}`)
	if found := address.FindString(body); found != "" {
		t.Errorf("settings payload contains an address-shaped string %q:\n%s", found, body)
	}
	for _, forbidden := range []string{
		"destination", "keypair", "token", "secret", "api-key", "/var/", "/home/", "http",
	} {
		if strings.Contains(strings.ToLower(body), forbidden) {
			t.Errorf("settings payload mentions %q, which it must not:\n%s", forbidden, body)
		}
	}
	// But it must still answer the questions it exists for.
	if !settings.Configured || settings.FundedTradesPerDay == 0 ||
		settings.PriceRule == "" || settings.DailyCap == "" {
		t.Errorf("settings payload answers nothing useful: %+v", settings)
	}
	if !settings.SweepConfigured || !settings.SweepProofValid {
		t.Errorf("settings must still say a sweep exists and is proven: %+v", settings)
	}
}
