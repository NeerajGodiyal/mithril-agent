package operatorstatus

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
)

const (
	Version       = 1
	maxStatusSize = 32 << 10
	StaleAfter    = 90 * time.Second
)

// Snapshot is the bounded, non-secret state exposed to operators and MCP
// clients. It intentionally excludes configuration, endpoints, and key data.
type Snapshot struct {
	Version        uint32         `json:"version"`
	ObservedAt     time.Time      `json:"observed_at"`
	Profile        string         `json:"profile"`
	ProfileVersion uint32         `json:"profile_version"`
	Cluster        string         `json:"cluster"`
	Result         Result         `json:"result"`
	LastAction     Action         `json:"last_action,omitzero"`
	Journal        journal.Stats  `json:"journal"`
	Control        control.Status `json:"control"`
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
	if err := control.ValidateStatus(currentControl); err != nil {
		return View{}, errors.New("current control status is invalid")
	}
	view := View{
		Profile:           profile,
		ProfileVersion:    profileVersion,
		Cluster:           cluster,
		RunnerState:       "not_started",
		Control:           currentControl,
		AttentionRequired: RequiresAttention(Result{}, currentControl, now.UTC()),
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
	if err := control.ValidateStatus(currentControl); err != nil {
		return View{}, errors.New("current control status is invalid")
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
	view.AttentionRequired = RequiresAttention(view.Result, currentControl, now)
	view.LastActionAcknowledged = terminalAction(view.LastAction.Result) &&
		currentControl.Mode == control.ModeNoNewActions &&
		currentControl.TerminalActionID == "" &&
		view.Result.ActionID != view.LastAction.Result.ActionID
	return view, nil
}

// RequiresAttention derives the bounded operator alarm state shared by status
// and metrics. Historical terminal actions do not alarm after acknowledgement.
func RequiresAttention(result Result, status control.Status, now time.Time) bool {
	if control.ValidateStatus(status) != nil || status.RecoveryPending ||
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
		snapshot.Journal.Records < 0 || snapshot.Journal.Bytes < 0 ||
		snapshot.Journal.ReservedBytes < 0 || snapshot.Journal.MaxRecords <= 0 ||
		snapshot.Journal.MaxBytes <= 0 {
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
	if err := control.ValidateStatus(snapshot.Control); err != nil {
		return errors.New("operator status control state is invalid")
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
