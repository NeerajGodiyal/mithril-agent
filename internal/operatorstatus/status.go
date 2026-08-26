package operatorstatus

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/Overclock-Validator/mithril-agent/agent"
	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/jupiterswap"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

const (
	Version       = 2
	maxStatusSize = 32 << 10
	StaleAfter    = 90 * time.Second
)

// Snapshot is the bounded, non-secret state exposed to operators and MCP
// clients. It intentionally excludes configuration, endpoints, and key data.
type Snapshot struct {
	Version        uint32             `json:"version"`
	ObservedAt     time.Time          `json:"observed_at"`
	Profile        string             `json:"profile"`
	ProfileVersion uint32             `json:"profile_version"`
	Cluster        string             `json:"cluster"`
	Result         Result             `json:"result"`
	LastAction     Action             `json:"last_action,omitzero"`
	Journal        journal.Stats      `json:"journal"`
	Control        control.Status     `json:"control"`
	Strategy       StrategyProjection `json:"strategy,omitzero"`
}

// StrategyProjection is the bounded, address-free subset of static policy an
// operator asks about. Numeric base units cross the socket; human formatting
// happens at the MCP edge, so untrusted status cannot inject labels or paths.
type StrategyProjection struct {
	Configured           bool      `json:"configured"`
	Direction            string    `json:"direction,omitempty"`
	InputAmount          uint64    `json:"input_amount,omitempty"`
	DailyCap             uint64    `json:"daily_cap,omitempty"`
	MaxFeeLamports       uint64    `json:"max_fee_lamports,omitempty"`
	FundedTradesPerDay   uint64    `json:"funded_trades_per_day,omitempty"`
	PriceDirection       string    `json:"price_direction,omitempty"`
	PriceThresholdMicros uint64    `json:"price_threshold_micros,omitempty"`
	SweepConfigured      bool      `json:"sweep_configured"`
	SweepProofValid      bool      `json:"sweep_proof_valid"`
	SweepKeepLamports    uint64    `json:"sweep_keep_lamports,omitempty"`
	SweepMaxLamports     uint64    `json:"sweep_max_lamports,omitempty"`
	SweepDailyLamports   uint64    `json:"sweep_daily_lamports,omitempty"`
	SweepActiveAfter     time.Time `json:"sweep_active_after,omitzero"`
}

type Action struct {
	ObservedAt time.Time `json:"observed_at"`
	Result     Result    `json:"result"`
}

// Result is the bounded, non-secret outcome shared by the runner and its
// read-only operator surfaces.
type Result struct {
	ActionID                     string               `json:"action_id,omitempty"`
	Decision                     string               `json:"decision"`
	Reason                       string               `json:"reason,omitempty"`
	AmountLamports               uint64               `json:"amount_lamports,omitempty"`
	InputAmount                  uint64               `json:"input_amount,omitempty"`
	InputAsset                   string               `json:"input_asset,omitempty"`
	OutputAsset                  string               `json:"output_asset,omitempty"`
	MinimumOutput                uint64               `json:"minimum_output,omitempty"`
	OutputAmount                 uint64               `json:"output_amount,omitempty"`
	Signature                    string               `json:"signature,omitempty"`
	Submitted                    bool                 `json:"submitted,omitempty"`
	Verdict                      string               `json:"verdict,omitempty"`
	Recovered                    bool                 `json:"recovered,omitempty"`
	PendingSinceUnix             int64                `json:"pending_since_unix,omitempty"`
	ReconciliationTimeoutSeconds uint64               `json:"reconciliation_timeout_seconds,omitempty"`
	PriceTrigger                 *pricetrigger.Status `json:"price_trigger,omitempty"`
	// BalanceLamports is the agent account balance from the cycle's validated
	// node observation, with the time it was observed. The pair travels
	// together: a balance without its observation time is unusable as
	// evidence, because the reader cannot tell current from stale.
	BalanceLamports     uint64 `json:"balance_lamports,omitempty"`
	BalanceObservedUnix int64  `json:"balance_observed_unix,omitempty"`
}

type View struct {
	Profile                string         `json:"profile"`
	ProfileVersion         uint32         `json:"profile_version"`
	Cluster                string         `json:"cluster"`
	RunnerState            string         `json:"runner_state"`
	ObservedAt             time.Time      `json:"observed_at,omitzero"`
	AgeSeconds             uint64         `json:"age_seconds,omitempty"`
	Stale                  bool           `json:"stale"`
	Result                 Result         `json:"result,omitzero"`
	LastAction             Action         `json:"last_action,omitzero"`
	Journal                journal.Stats  `json:"journal,omitzero"`
	Control                control.Status `json:"control"`
	AttentionRequired      bool           `json:"attention_required"`
	LastActionAcknowledged bool           `json:"last_action_acknowledged,omitempty"`
}

func Path(journalPath string) string {
	return journalPath + ".status.json"
}

func Write(path string, snapshot Snapshot) error {
	snapshot = retainLastAction(path, snapshot)
	if err := validate(snapshot); err != nil {
		return err
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return errors.New("encode operator status")
	}
	return securefile.ReplacePrivate(path, append(encoded, '\n'), maxStatusSize)
}

func Read(path string) (Snapshot, error) {
	var data []byte
	var err error
	for range 3 {
		data, err = securefile.ReadPrivate(path, maxStatusSize)
		if !errors.Is(err, securefile.ErrChanged) {
			break
		}
	}
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Snapshot{}, os.ErrNotExist
		}
		return Snapshot{}, fmt.Errorf("read operator status: %w", err)
	}
	var snapshot Snapshot
	if err := strictjson.Decode(data, &snapshot); err != nil {
		return Snapshot{}, errors.New("decode operator status")
	}
	if err := validate(snapshot); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

// ValidateSnapshot validates an already decoded bounded operator projection.
// It is used by read-only transports that must revalidate untrusted wire data.
func ValidateSnapshot(snapshot Snapshot) error {
	return validate(snapshot)
}

func CurrentView(
	path,
	profile,
	cluster string,
	profileVersion uint32,
	currentControl control.Status,
	now time.Time,
) (View, error) {
	if err := validateControlStatus(cluster, currentControl); err != nil {
		return View{}, errors.New("current control status is invalid")
	}
	if err := validateProfileControl(profile, profileVersion, cluster, currentControl); err != nil {
		return View{}, err
	}
	view := View{
		Profile:        profile,
		ProfileVersion: profileVersion,
		Cluster:        cluster,
		RunnerState:    "not_started",
		Control:        currentControl,
		AttentionRequired: RequiresAttentionForCluster(
			Result{}, currentControl, cluster, now.UTC(),
		),
	}
	snapshot, err := Read(path)
	if errors.Is(err, os.ErrNotExist) {
		return view, nil
	}
	if err != nil {
		return View{}, err
	}
	if snapshot.Profile != profile || snapshot.ProfileVersion != profileVersion ||
		snapshot.Cluster != cluster {
		return View{}, errors.New("operator status does not match the active profile")
	}
	return viewFromSnapshot(snapshot, currentControl, now)
}

// ViewFromSnapshot derives the bounded operator view used by transports that
// receive a validated status snapshot instead of the private agent config.
func ViewFromSnapshot(snapshot Snapshot, now time.Time) (View, error) {
	if err := validate(snapshot); err != nil {
		return View{}, err
	}
	return viewFromSnapshot(snapshot, snapshot.Control, now)
}

func viewFromSnapshot(
	snapshot Snapshot,
	currentControl control.Status,
	now time.Time,
) (View, error) {
	if err := validateControlStatus(snapshot.Cluster, currentControl); err != nil {
		return View{}, errors.New("current control status is invalid")
	}
	if err := validateProfileControl(
		snapshot.Profile, snapshot.ProfileVersion, snapshot.Cluster, currentControl,
	); err != nil {
		return View{}, err
	}
	now = now.UTC()
	if now.IsZero() {
		return View{}, errors.New("operator status clock is unavailable")
	}
	if snapshot.ObservedAt.After(now.Add(5 * time.Second)) {
		return View{}, errors.New("operator status timestamp is in the future")
	}
	age := now.Sub(snapshot.ObservedAt)
	if age < 0 {
		age = 0
	}
	view := View{
		Profile: snapshot.Profile, ProfileVersion: snapshot.ProfileVersion,
		Cluster: snapshot.Cluster, RunnerState: "recent", Control: currentControl,
	}
	view.ObservedAt = snapshot.ObservedAt
	view.AgeSeconds = uint64(age / time.Second)
	view.Stale = age > StaleAfter
	if view.Stale {
		view.RunnerState = "stale"
	}
	view.Result = snapshot.Result
	view.LastAction = snapshot.LastAction
	view.Journal = snapshot.Journal
	view.AttentionRequired = RequiresAttentionForCluster(
		view.Result, currentControl, snapshot.Cluster, now,
	)
	view.LastActionAcknowledged = terminalAction(view.LastAction.Result) &&
		currentControl.Mode == control.ModeNoNewActions &&
		currentControl.TerminalActionID == "" &&
		view.Result.ActionID != view.LastAction.Result.ActionID
	return view, nil
}

// RequiresAttention derives the bounded operator alarm state shared by status
// and metrics. Historical terminal actions do not alarm after acknowledgement.
func RequiresAttention(result Result, status control.Status, now time.Time) bool {
	return RequiresAttentionForCluster(result, status, "devnet", now)
}

// RequiresAttentionForCluster derives the operator alarm state while binding
// an active control mode to its cluster.
func RequiresAttentionForCluster(
	result Result,
	status control.Status,
	cluster string,
	now time.Time,
) bool {
	if validateControlStatus(cluster, status) != nil || status.RecoveryPending ||
		status.TerminalActionID != "" {
		return true
	}
	switch result.Decision {
	case "degraded", "failed", "halted":
		return true
	case "canceled":
		return result.Submitted
	case "executing", "pending":
		if result.PendingSinceUnix <= 0 || result.ReconciliationTimeoutSeconds == 0 {
			return true
		}
		if now.IsZero() || result.PendingSinceUnix > now.UTC().Unix() {
			return true
		}
		return uint64(now.UTC().Unix()-result.PendingSinceUnix) >
			result.ReconciliationTimeoutSeconds
	case "", "complete", "skipped", "stopped", "waiting":
		return false
	default:
		return true
	}
}

func terminalAction(result Result) bool {
	return result.ActionID != "" &&
		(result.Decision == "failed" || result.Decision == "halted")
}

func retainLastAction(path string, snapshot Snapshot) Snapshot {
	if snapshot.Result.ActionID != "" {
		if snapshot.LastAction.Result.ActionID != snapshot.Result.ActionID {
			snapshot.LastAction = Action{ObservedAt: snapshot.ObservedAt, Result: snapshot.Result}
		}
	}
	if snapshot.LastAction.Result.ActionID != "" {
		return snapshot
	}
	previous, err := Read(path)
	if err != nil || previous.Profile != snapshot.Profile ||
		previous.ProfileVersion != snapshot.ProfileVersion ||
		previous.Cluster != snapshot.Cluster {
		return snapshot
	}
	if previous.LastAction.Result.ActionID != "" {
		snapshot.LastAction = previous.LastAction
	} else if previous.Result.ActionID != "" {
		snapshot.LastAction = Action{ObservedAt: previous.ObservedAt, Result: previous.Result}
	}
	return snapshot
}

func validate(snapshot Snapshot) error {
	if snapshot.Version != Version || snapshot.ObservedAt.IsZero() ||
		snapshot.Profile == "" || snapshot.ProfileVersion == 0 ||
		snapshot.Cluster == "" || snapshot.Result.Decision == "" ||
		snapshot.Journal.ActiveRecords < 0 || snapshot.Journal.Records < 0 ||
		snapshot.Journal.Bytes < 0 ||
		snapshot.Journal.ReservedBytes < 0 || snapshot.Journal.MaxRecords <= 0 ||
		snapshot.Journal.MaxBytes <= 0 ||
		snapshot.Journal.ActiveRecords > snapshot.Journal.Records ||
		snapshot.Journal.ActiveRecords > snapshot.Journal.MaxRecords ||
		snapshot.Journal.Bytes > snapshot.Journal.MaxBytes ||
		snapshot.Journal.ReservedBytes > snapshot.Journal.MaxBytes ||
		snapshot.Journal.Bytes > snapshot.Journal.MaxBytes-snapshot.Journal.ReservedBytes ||
		snapshot.Journal.SendStartedRecords < 0 ||
		snapshot.Journal.SubmittedRecords < 0 ||
		snapshot.Journal.SendStartedRecords > snapshot.Journal.Records ||
		snapshot.Journal.SubmittedRecords > snapshot.Journal.SendStartedRecords {
		return errors.New("operator status is invalid")
	}
	if snapshot.LastAction != (Action{}) &&
		(snapshot.LastAction.ObservedAt.IsZero() || snapshot.LastAction.Result.ActionID == "" ||
			snapshot.LastAction.Result.Decision == "") {
		return errors.New("operator status last action is invalid")
	}
	if err := validateResult(snapshot.Profile, snapshot.Result); err != nil {
		return err
	}
	if snapshot.LastAction != (Action{}) {
		if err := validateResult(snapshot.Profile, snapshot.LastAction.Result); err != nil {
			return errors.New("operator status last action result is invalid")
		}
	}
	if err := validateProfileControl(
		snapshot.Profile, snapshot.ProfileVersion, snapshot.Cluster, snapshot.Control,
	); err != nil {
		return err
	}
	if err := validateControlStatus(snapshot.Cluster, snapshot.Control); err != nil {
		return errors.New("operator status control state is invalid")
	}
	if err := validateStrategyProjection(
		snapshot.Profile, snapshot.ProfileVersion, snapshot.Cluster, snapshot.Strategy,
	); err != nil {
		return err
	}
	return nil
}

func validateProfileControl(
	profile string,
	profileVersion uint32,
	cluster string,
	status control.Status,
) error {
	validIdentity := false
	switch profile {
	case agent.ProfileTreasurySweepV1:
		validIdentity = profileVersion == 1 &&
			(cluster == "devnet" || cluster == "testnet" || cluster == "mainnet-beta")
	case orcaswap.ProfileName:
		validIdentity = profileVersion == orcaswap.ProfileVersion && cluster == "devnet"
	case orcaswap.BuyProfileName:
		validIdentity = profileVersion == orcaswap.BuyProfileVersion && cluster == "devnet"
	case jupiterswap.ProfileName:
		validIdentity = profileVersion == jupiterswap.ProfileVersion && cluster == "mainnet-beta"
	}
	if !validIdentity {
		return errors.New("operator status profile does not match its version or cluster")
	}
	if status.Mode == control.ModeMainnetCanary && profile != jupiterswap.ProfileName {
		return errors.New("Mainnet canary control status does not match the Jupiter profile")
	}
	return nil
}

func validateControlStatus(cluster string, status control.Status) error {
	switch status.Mode {
	case control.ModeDevnetEnabled:
		if cluster != "devnet" {
			return errors.New("Devnet control status does not match cluster")
		}
		return control.ValidateStatus(status)
	case control.ModeMainnetCanary:
		if cluster != "mainnet-beta" {
			return errors.New("Mainnet control status does not match cluster")
		}
		return control.ValidateMainnetCanaryStatus(status)
	default:
		return control.ValidateStatus(status)
	}
}

// ValidateStrategyProjection validates the same bounded strategy contract for
// config-backed MCP providers that do not pass through a Snapshot.
func ValidateStrategyProjection(
	profile string,
	profileVersion uint32,
	cluster string,
	strategy StrategyProjection,
) error {
	return validateStrategyProjection(profile, profileVersion, cluster, strategy)
}

func validateStrategyProjection(
	profile string,
	profileVersion uint32,
	cluster string,
	strategy StrategyProjection,
) error {
	if !strategy.Configured {
		if strategy != (StrategyProjection{}) {
			return errors.New("unconfigured operator strategy contains settings")
		}
		return nil
	}
	wantDirection := ""
	var wantVersion uint32
	switch profile {
	case orcaswap.ProfileName:
		wantDirection = "sell"
		wantVersion = orcaswap.ProfileVersion
	case orcaswap.BuyProfileName:
		wantDirection = "buy"
		wantVersion = orcaswap.BuyProfileVersion
	default:
		return errors.New("operator strategy does not match a swap profile")
	}
	if profileVersion != wantVersion || cluster != "devnet" {
		return errors.New("operator strategy does not match the profile version or cluster")
	}
	if strategy.Direction != wantDirection || strategy.InputAmount == 0 ||
		strategy.DailyCap < strategy.InputAmount || strategy.MaxFeeLamports == 0 ||
		strategy.FundedTradesPerDay == 0 {
		return errors.New("operator strategy spending bounds are invalid")
	}
	if strategy.FundedTradesPerDay > strategy.DailyCap/strategy.InputAmount {
		return errors.New("operator strategy funded-trade count exceeds its daily cap")
	}
	if strategy.PriceDirection == "" {
		if strategy.PriceThresholdMicros != 0 {
			return errors.New("operator strategy price rule is incomplete")
		}
	} else {
		wantPriceDirection := "sell_at_or_above"
		if wantDirection == "buy" {
			wantPriceDirection = "buy_at_or_below"
		}
		if strategy.PriceDirection != wantPriceDirection || strategy.PriceThresholdMicros == 0 {
			return errors.New("operator strategy price rule does not match the profile")
		}
	}
	if !strategy.SweepConfigured {
		withoutSweep := strategy
		withoutSweep.Configured = false
		withoutSweep.Direction = ""
		withoutSweep.InputAmount = 0
		withoutSweep.DailyCap = 0
		withoutSweep.MaxFeeLamports = 0
		withoutSweep.FundedTradesPerDay = 0
		withoutSweep.PriceDirection = ""
		withoutSweep.PriceThresholdMicros = 0
		if withoutSweep != (StrategyProjection{}) {
			return errors.New("operator strategy has sweep settings without a sweep")
		}
		return nil
	}
	if strategy.SweepMaxLamports == 0 ||
		strategy.SweepDailyLamports < strategy.SweepMaxLamports ||
		strategy.SweepActiveAfter.IsZero() {
		return errors.New("operator strategy sweep bounds are invalid")
	}
	return nil
}

func validateResult(profile string, result Result) error {
	if (result.InputAsset == "") != (result.OutputAsset == "") ||
		(result.InputAsset != "" && (result.InputAmount == 0 || result.InputAsset == result.OutputAsset)) {
		return errors.New("operator trade assets are invalid")
	}
	if result.InputAsset != "" {
		wantInput, wantOutput := "", ""
		switch profile {
		case orcaswap.ProfileName:
			wantInput, wantOutput = "SOL", "devUSDC"
		case orcaswap.BuyProfileName:
			wantInput, wantOutput = "devUSDC", "SOL"
		}
		if wantInput != "" &&
			(result.InputAsset != wantInput || result.OutputAsset != wantOutput) {
			return errors.New("operator trade assets do not match the active profile")
		}
	}
	if isTradeLifecycleResult(profile, result) {
		if result.InputAmount == 0 || result.ReconciliationTimeoutSeconds == 0 {
			return errors.New("operator trade lifecycle is incomplete")
		}
		switch profile {
		case orcaswap.ProfileName:
			if result.InputAsset != "SOL" || result.OutputAsset != "devUSDC" ||
				result.AmountLamports != result.InputAmount {
				return errors.New("operator sell result does not match the active profile")
			}
		case orcaswap.BuyProfileName:
			if result.InputAsset != "devUSDC" || result.OutputAsset != "SOL" ||
				result.AmountLamports != 0 {
				return errors.New("operator buy result does not match the active profile")
			}
		}
		switch result.Decision {
		case "canceled":
			if result.Verdict != "" || result.Submitted || result.OutputAmount != 0 ||
				result.PendingSinceUnix != 0 {
				return errors.New("operator canceled trade evidence is invalid")
			}
		case "pending":
			if result.Verdict != "pending" || result.Signature == "" ||
				result.MinimumOutput == 0 || result.PendingSinceUnix <= 0 {
				return errors.New("operator pending trade evidence is invalid")
			}
		case "complete":
			if result.Verdict != "finalized" || !result.Submitted ||
				result.Signature == "" || result.MinimumOutput == 0 ||
				result.OutputAmount < result.MinimumOutput || result.PendingSinceUnix <= 0 {
				return errors.New("operator completed trade evidence is invalid")
			}
		case "failed":
			if result.Verdict != "failed" || !result.Submitted ||
				result.Signature == "" || result.MinimumOutput == 0 ||
				result.PendingSinceUnix <= 0 {
				return errors.New("operator failed trade evidence is invalid")
			}
		case "halted":
			if (result.Verdict != "unresolved" && result.Verdict != "diverged") ||
				!result.Submitted || result.Signature == "" ||
				result.MinimumOutput == 0 || result.PendingSinceUnix <= 0 {
				return errors.New("operator halted trade evidence is invalid")
			}
		}
	}
	if result.PriceTrigger != nil {
		if err := pricetrigger.ValidateStatus(*result.PriceTrigger); err != nil {
			return errors.New("operator price trigger status is invalid")
		}
	}
	if result.BalanceObservedUnix < 0 ||
		(result.BalanceLamports != 0 && result.BalanceObservedUnix == 0) {
		return errors.New("operator balance observation is invalid")
	}
	return nil
}

func isTradeLifecycleResult(profile string, result Result) bool {
	if result.ActionID == "" ||
		(profile != orcaswap.ProfileName && profile != orcaswap.BuyProfileName) {
		return false
	}
	switch result.Decision {
	case "pending", "canceled", "complete", "failed", "halted":
		return true
	default:
		return false
	}
}

// FormatAmount renders the two fixed pilot assets for operator-facing text.
// Unknown assets retain their raw base-unit representation.
func FormatAmount(amount uint64, asset string) string {
	decimals := 0
	switch asset {
	case "SOL":
		decimals = 9
	case "devUSDC":
		decimals = 6
	default:
		if asset == "" {
			return fmt.Sprintf("%d base units", amount)
		}
		return fmt.Sprintf("%d %s base units", amount, asset)
	}
	scale := uint64(1)
	for range decimals {
		scale *= 10
	}
	return fmt.Sprintf("%d.%0*d %s", amount/scale, decimals, amount%scale, asset)
}
