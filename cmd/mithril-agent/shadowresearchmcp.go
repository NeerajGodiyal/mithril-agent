package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Overclock-Validator/mithril-agent/internal/mcpstdio"
	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/shadow"
	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	shadowPaperHypothesisVersion = uint32(1)
	shadowHypothesisMaxSources   = 8
	shadowResearchMaxCandidates  = 1024
	shadowResearchDefaultQuota   = 64
)

const shadowResearchMCPUsage = `Usage: mithril-agent shadow research-mcp --policy PATH
       --journal-dir PATH --candidate-dir PATH --challenger-pointer PATH
       --champion-pointer PATH --champion-dir PATH --challenger-dir PATH
       --challenge-days N
       [--base-policy PATH] [--spread-bps N] [--max-candidates N]

Serves two local MCP tools that create a bounded, cited paper challenger and
report its paired evidence status using only operator-fixed inputs. They may
read the paper champion but atomically update only the paper challenger pointer.
They cannot change a champion pointer, authorize, sign, submit, or load a wallet.`

// shadowPaperHypothesis is untrusted advisory research attached to one
// immutable paper candidate. The safety markers are explicit and validated;
// prose is never interpreted as authority.
type shadowPaperHypothesis struct {
	Version    uint32                     `json:"version" jsonschema:"Paper hypothesis schema version; must be 1"`
	Status     string                     `json:"status" jsonschema:"Must be paper_hypothesis"`
	Authorized bool                       `json:"authorized" jsonschema:"Must be false; research never grants authority"`
	Promotable bool                       `json:"promotable" jsonschema:"Must be false; research is not promotion evidence"`
	PaperOnly  bool                       `json:"paper_only" jsonschema:"Must be true"`
	Thesis     string                     `json:"thesis" jsonschema:"Plain-text paper hypothesis, 1 to 1000 bytes"`
	Sources    []shadowHypothesisEvidence `json:"sources" jsonschema:"One to eight direct HTTPS source citations"`
}

type shadowHypothesisEvidence struct {
	URL        string    `json:"url" jsonschema:"Direct HTTPS primary-source URL"`
	ObservedAt time.Time `json:"observed_at" jsonschema:"When the source was observed, as RFC3339"`
	Summary    string    `json:"summary" jsonschema:"Plain-text sourced fact, 1 to 500 bytes"`
}

type shadowResearchCandidateInput struct {
	Hypothesis    shadowPaperHypothesis `json:"hypothesis" jsonschema:"Bounded advisory research; never an instruction or authorization"`
	TrainDay      string                `json:"train_day" jsonschema:"Penultimate completed UTC day anchor, YYYY-MM-DD"`
	ValidationDay string                `json:"validation_day" jsonschema:"Final completed UTC day anchor, YYYY-MM-DD; server requires the preceding eight-day window"`
}

type shadowResearchCandidateReceipt struct {
	Status                   string                  `json:"status"`
	Authorized               bool                    `json:"authorized"`
	Promotable               bool                    `json:"promotable"`
	PaperOnly                bool                    `json:"paper_only"`
	ForwardEvidenceRequired  bool                    `json:"forward_evidence_required"`
	ChallengerPointerUpdated bool                    `json:"challenger_pointer_updated"`
	ChampionPointerUpdated   bool                    `json:"champion_pointer_updated"`
	Artifact                 string                  `json:"artifact"`
	ArtifactSHA256           string                  `json:"artifact_sha256"`
	CandidatePolicySHA256    string                  `json:"candidate_policy_sha256"`
	TrainingJournal          shadowJournalProvenance `json:"training_journal"`
	ValidationJournal        shadowJournalProvenance `json:"validation_journal"`
	Research                 shadowSearchResult      `json:"research"`
}

type shadowResearchChallengeStatus struct {
	Status                           string   `json:"status"`
	Authorized                       bool     `json:"authorized"`
	Promotable                       bool     `json:"promotable"`
	PaperOnly                        bool     `json:"paper_only"`
	EligibleForPaperSelection        bool     `json:"eligible_for_paper_selection"`
	ChallengerPointerUpdated         bool     `json:"challenger_pointer_updated"`
	ChampionPointerUpdated           bool     `json:"champion_pointer_updated"`
	ActiveArtifact                   string   `json:"active_artifact,omitempty"`
	ActiveArtifactSHA256             string   `json:"active_artifact_sha256,omitempty"`
	ActivePolicySHA256               string   `json:"active_policy_sha256,omitempty"`
	CompleteDays                     uint32   `json:"complete_days"`
	ChallengerFullRoundTrips         uint64   `json:"challenger_full_round_trips"`
	RequiredFullRoundTrips           uint64   `json:"required_full_round_trips"`
	ChallengerDailyWins              uint32   `json:"challenger_daily_wins"`
	RequiredDailyWins                uint32   `json:"required_daily_wins"`
	AggregateAdvantageMicros         int64    `json:"aggregate_advantage_micros"`
	RequiredAggregateAdvantageMicros uint64   `json:"required_aggregate_advantage_micros"`
	Reasons                          []string `json:"reasons,omitempty"`
}

type shadowResearchStatusInput struct{}

type shadowResearchController struct {
	policy            shadow.Policy
	basePolicy        shadow.Policy
	journalDir        string
	candidateDir      string
	challengerPointer string
	lifecycleLock     string
	championPointer   string
	championRoot      string
	challengerRoot    string
	spreadBPS         uint64
	maxCandidates     int
	challengeDays     uint32
	now               func() time.Time
}

var shadowResearchAfterCandidateArtifact = func() {}

func runShadowResearchMCP(
	ctx context.Context,
	args []string,
	input io.Reader,
	output io.Writer,
) error {
	flags := flag.NewFlagSet("shadow research-mcp", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	policyPath := flags.String("policy", "", "observed shadow policy")
	basePolicyPath := flags.String("base-policy", "", "immutable original base policy")
	journalDir := flags.String("journal-dir", "", "operator-fixed completed journal directory")
	candidateDir := flags.String("candidate-dir", "", "operator-fixed immutable challenger directory")
	challengerPointer := flags.String("challenger-pointer", "", "operator-fixed paper challenger pointer")
	championPointer := flags.String("champion-pointer", "", "operator-fixed paper champion pointer")
	championRoot := flags.String("champion-dir", "", "operator-fixed champion run root")
	challengerRoot := flags.String("challenger-dir", "", "operator-fixed challenger run root")
	challengeDays := flags.Uint("challenge-days", 7, "paired complete UTC days, 7..3650")
	spreadBPS := flags.Uint64("spread-bps", 100, "operator-fixed modelled pool cost")
	maxCandidates := flags.Int("max-candidates", shadowResearchDefaultQuota, "maximum immutable challengers")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, shadowResearchMCPUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *policyPath == "" || *journalDir == "" || *candidateDir == "" ||
		*challengerPointer == "" || *championPointer == "" || *championRoot == "" ||
		*challengerRoot == "" || *challengeDays < 7 || *challengeDays > 3650 {
		return errors.New("shadow research-mcp requires fixed policy, journal, candidate, challenger pointer, champion pointer, and paired run paths")
	}
	closer, ok := input.(io.ReadCloser)
	if !ok {
		return errors.New("shadow research MCP input must be closable stdio")
	}
	controller, err := newShadowResearchController(
		*policyPath, *basePolicyPath, *journalDir, *candidateDir, *challengerPointer,
		*championPointer, *championRoot, *challengerRoot,
		*spreadBPS, *maxCandidates, uint32(*challengeDays),
	)
	if err != nil {
		return err
	}
	return serveShadowResearchMCP(ctx, controller, closer, output)
}

func newShadowResearchController(
	policyPath, basePolicyPath, journalDir, candidateDir, challengerPointer,
	championPointer, championRoot, challengerRoot string,
	spreadBPS uint64, maxCandidates int, challengeDays uint32,
) (*shadowResearchController, error) {
	if spreadBPS == 0 || spreadBPS >= 10_000 {
		return nil, errors.New("shadow research MCP spread must be between 1 and 9999 basis points")
	}
	if maxCandidates <= 0 || maxCandidates > shadowResearchMaxCandidates {
		return nil, fmt.Errorf("shadow research MCP max candidates must be between 1 and %d", shadowResearchMaxCandidates)
	}
	if challengeDays < 7 || challengeDays > 3650 {
		return nil, errors.New("shadow research MCP challenge days must be between 7 and 3650")
	}
	policy, err := loadActiveShadowPolicy(policyPath)
	if err != nil {
		return nil, err
	}
	basePolicy := policy
	if basePolicyPath != "" {
		basePolicy, err = loadActiveShadowPolicy(basePolicyPath)
		if err != nil {
			return nil, err
		}
		if err := validateShadowSearchLineage(basePolicy, policy); err != nil {
			return nil, err
		}
	}
	if basePolicy.Cluster != shadow.Mainnet {
		return nil, errors.New("shadow research MCP lifecycle requires a Mainnet paper policy")
	}
	for _, root := range []string{journalDir, candidateDir} {
		if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root ||
			validatePrivateDirectory(root) != nil {
			return nil, errors.New("shadow research MCP roots must be distinct absolute private directories with mode 0700")
		}
	}
	if journalDir == candidateDir {
		return nil, errors.New("shadow research MCP roots must be distinct absolute private directories with mode 0700")
	}
	if challengerPointer == "" || !filepath.IsAbs(challengerPointer) ||
		filepath.Clean(challengerPointer) != challengerPointer ||
		challengerPointer == policyPath || challengerPointer == basePolicyPath ||
		pathWithinShadowResearchRoot(challengerPointer, journalDir) ||
		pathWithinShadowResearchRoot(challengerPointer, candidateDir) ||
		validatePrivateDirectory(filepath.Dir(challengerPointer)) != nil {
		return nil, errors.New("shadow research MCP challenger pointer must be a clean absolute path outside the candidate directory with a private 0700 parent")
	}
	lifecycleLock := filepath.Join(filepath.Dir(challengerPointer), "lifecycle.lock")
	if lifecycleLock == challengerPointer || lifecycleLock == championPointer ||
		lifecycleLock == policyPath || lifecycleLock == basePolicyPath {
		return nil, errors.New("shadow research MCP lifecycle lock conflicts with a fixed input")
	}
	if championPointer == challengerPointer || !absoluteClean(championPointer) ||
		!absoluteClean(championRoot) || !absoluteClean(challengerRoot) ||
		championRoot == challengerRoot || validatePrivateDirectory(championRoot) != nil ||
		validatePrivateDirectory(challengerRoot) != nil {
		return nil, errors.New("shadow research MCP needs distinct fixed private champion and challenger evidence paths")
	}
	if _, _, _, err := loadBoundSelectedShadowCandidate(championPointer, basePolicy); err != nil {
		return nil, errors.New("shadow research MCP champion pointer is invalid")
	}
	if _, err := securefile.ReadPrivate(challengerPointer, shadowCandidatePointerBytes); err != nil &&
		!errors.Is(err, os.ErrNotExist) {
		return nil, errors.New("shadow research MCP challenger pointer must be absent or a private regular file")
	} else if err == nil {
		_, activePath, _, selected, loadErr := loadBoundShadowCandidateSelection(
			challengerPointer, basePolicy,
		)
		if loadErr != nil || selected.SelectedAt == nil || selected.EligibleFrom == nil ||
			selected.ChallengeDays != challengeDays ||
			selected.ChallengeGateVersion != shadowChallengeGateVersion ||
			filepath.Dir(activePath) != candidateDir {
			return nil, errors.New("shadow research MCP existing challenger pointer is not a bound research selection")
		}
	}
	return &shadowResearchController{
		policy: policy, basePolicy: basePolicy,
		journalDir: journalDir, candidateDir: candidateDir,
		challengerPointer: challengerPointer, lifecycleLock: lifecycleLock, spreadBPS: spreadBPS,
		championPointer: championPointer, championRoot: championRoot,
		challengerRoot: challengerRoot, maxCandidates: maxCandidates,
		challengeDays: challengeDays,
		now:           func() time.Time { return time.Now().UTC() },
	}, nil
}

func pathWithinShadowResearchRoot(path, root string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func serveShadowResearchMCP(
	ctx context.Context,
	controller *shadowResearchController,
	input io.ReadCloser,
	output io.Writer,
) error {
	if controller == nil || input == nil || output == nil {
		return errors.New("shadow research MCP controller and stdio are required")
	}
	server := mcpsdk.NewServer(&mcpsdk.Implementation{
		Name: "mithril-paper-research", Title: "Mithril paper challenger research", Version: "0.1.0",
	}, &mcpsdk.ServerOptions{Instructions: "Create only immutable, unauthorized paper challenger artifacts from operator-fixed policies and completed journals, then update only the operator-fixed paper challenger pointer. This server may read but cannot change the paper champion pointer, and has no wallet, signer, submitter, live policy, terminal, or network authority."})
	server.AddReceivingMiddleware(mcpstdio.LimitToolCalls(1))
	closedWorld, nonDestructive := false, false
	createAnnotations := &mcpsdk.ToolAnnotations{
		ReadOnlyHint: false, DestructiveHint: &nonDestructive,
		IdempotentHint: true, OpenWorldHint: &closedWorld,
	}
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name: "mithril_paper_create_challenger", Title: "Create Paper Challenger",
		Description: "Schema-validate and attach a cited paper-only hypothesis, require seven chronological train/out-of-sample folds from eight consecutive completed journals, write one immutable challenger artifact, and atomically update only the dedicated paper challenger pointer. Never selects a champion or promotes to live trading.",
		Annotations: createAnnotations,
	}, func(_ context.Context, _ *mcpsdk.CallToolRequest, input shadowResearchCandidateInput) (*mcpsdk.CallToolResult, shadowResearchCandidateReceipt, error) {
		result, err := controller.createCandidate(input, controller.now())
		return nil, result, err
	})
	statusAnnotations := &mcpsdk.ToolAnnotations{
		ReadOnlyHint: true, DestructiveHint: &nonDestructive,
		IdempotentHint: true, OpenWorldHint: &closedWorld,
	}
	mcpsdk.AddTool(server, &mcpsdk.Tool{
		Name: "mithril_paper_challenge_status", Title: "Paper Challenge Status",
		Description: "Read the operator-fixed active paper challenger and report its paired forward evidence state. Never changes either pointer or any policy.",
		Annotations: statusAnnotations,
	}, func(_ context.Context, _ *mcpsdk.CallToolRequest, _ shadowResearchStatusInput) (*mcpsdk.CallToolResult, shadowResearchChallengeStatus, error) {
		result, err := controller.challengeStatus(controller.now())
		return nil, result, err
	})
	err := server.Run(ctx, &mcpsdk.IOTransport{
		Reader: mcpstdio.NewReader(input), Writer: mcpstdio.WriteCloser{Writer: output},
	})
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) ||
		errors.Is(err, mcpsdk.ErrConnectionClosed) || err.Error() == "server is closing" ||
		err.Error() == "server is closing: EOF" {
		return nil
	}
	return err
}

func (controller *shadowResearchController) createCandidate(
	input shadowResearchCandidateInput,
	now time.Time,
) (shadowResearchCandidateReceipt, error) {
	if err := input.validate(now); err != nil {
		return shadowResearchCandidateReceipt{}, err
	}
	days, err := readShadowWalkForwardDays(
		controller.journalDir, input.ValidationDay, controller.policy,
	)
	if err != nil {
		return shadowResearchCandidateReceipt{}, err
	}
	result, err := searchShadowWalkForward(controller.policy, days, controller.spreadBPS)
	if err != nil {
		return shadowResearchCandidateReceipt{}, err
	}
	training := days[len(days)-2].Provenance
	validation := days[len(days)-1].Provenance
	if result.WalkForward == nil {
		return shadowResearchCandidateReceipt{}, errors.New("paper challenger has no walk-forward admission evidence")
	}
	candidate, err := newShadowPaperCandidate(controller.basePolicy, result, training, validation)
	if err != nil {
		return shadowResearchCandidateReceipt{}, err
	}
	candidate.Hypothesis = &input.Hypothesis
	if err := candidate.validateAgainst(controller.basePolicy); err != nil {
		return shadowResearchCandidateReceipt{}, err
	}
	encoded, err := encodeShadowPaperCandidate(candidate)
	if err != nil {
		return shadowResearchCandidateReceipt{}, err
	}
	digest := sha256.Sum256(encoded)
	artifactSHA256 := hex.EncodeToString(digest[:])
	artifact := "challenger-" + artifactSHA256 + ".json"
	artifactPath := filepath.Join(controller.candidateDir, artifact)
	var pointerState shadowResearchPointerState
	if err := withShadowLifecycleLock(controller.lifecycleLock, func() error {
		var err error
		pointerState, err = controller.candidateRotationState(
			artifactPath, artifactSHA256, candidate.CandidatePolicySHA256, now,
		)
		if err != nil {
			return err
		}
		if err := controller.ensureCandidateArtifact(artifactPath, encoded); err != nil {
			return errors.New("could not write the immutable paper challenger")
		}
		shadowResearchAfterCandidateArtifact()
		if err := controller.verifyChallengerPointerState(pointerState); err != nil {
			return err
		}
		if pointerState.sameArtifact {
			return nil
		}
		if err := replaceShadowCandidatePointerSelected(
			controller.challengerPointer, artifactPath, artifactSHA256,
			candidate.CandidatePolicySHA256, now, controller.challengeDays,
		); err != nil {
			return errors.New("could not atomically update the paper challenger pointer")
		}
		return nil
	}); err != nil {
		return shadowResearchCandidateReceipt{}, err
	}
	return shadowResearchCandidateReceipt{
		Status: "paper_challenger_ready", PaperOnly: true,
		ForwardEvidenceRequired:  true,
		ChallengerPointerUpdated: !pointerState.sameArtifact,
		ChampionPointerUpdated:   false,
		Artifact:                 artifact, ArtifactSHA256: artifactSHA256,
		CandidatePolicySHA256: candidate.CandidatePolicySHA256,
		TrainingJournal:       training, ValidationJournal: validation, Research: result,
	}, nil
}

func readShadowWalkForwardDays(
	directory, finalValidationDay string, policy shadow.Policy,
) ([]shadowWalkForwardDay, error) {
	finalDay, err := time.Parse("2006-01-02", finalValidationDay)
	if err != nil {
		return nil, errors.New("walk-forward validation day is invalid")
	}
	days := make([]shadowWalkForwardDay, 0, shadowWalkForwardWindows+1)
	for offset := -shadowWalkForwardWindows; offset <= 0; offset++ {
		dayStart := finalDay.AddDate(0, 0, offset)
		day := dayStart.Format("2006-01-02")
		ticks, provenance, err := readShadowSearchJournal(
			filepath.Join(directory, "shadow-"+day+".jsonl"), day, policy,
		)
		if err != nil {
			return nil, fmt.Errorf("read walk-forward journal %s: %w", day, err)
		}
		replayed, err := shadow.Replay(policy, ticks)
		if err != nil {
			return nil, fmt.Errorf("replay walk-forward journal %s: %w", day, err)
		}
		report, err := shadow.BuildReport(
			policy, replayed.Ledger, replayed.Counts, replayed.Stats,
			replayed.ClosingPrice, dayStart, replayed.PeriodEnd,
		)
		coverage := shadowWalkForwardObservableBPS(ticks, dayStart, policy.Tick())
		report.ObservableBPS = coverage
		if err != nil || !report.Trustworthy() {
			return nil, fmt.Errorf("walk-forward journal %s lacks 95%% observable coverage", day)
		}
		days = append(days, shadowWalkForwardDay{
			Ticks: ticks, Provenance: provenance, ObservableBPS: coverage,
		})
	}
	return days, nil
}

func shadowWalkForwardObservableBPS(
	ticks []shadow.Tick, dayStart time.Time, interval time.Duration,
) int32 {
	dayEnd := dayStart.Add(24 * time.Hour)
	expected := uint64((24*time.Hour + interval - 1) / interval)
	seen := make(map[uint64]struct{}, expected)
	for _, tick := range ticks {
		if tick.PeriodClose || tick.Event == shadow.EventUnobservable ||
			tick.PriceMicros == 0 || tick.At.Before(dayStart) || !tick.At.Before(dayEnd) {
			continue
		}
		seen[uint64(tick.At.Sub(dayStart)/interval)] = struct{}{}
	}
	return int32(uint64(len(seen)) * 10_000 / expected)
}

type shadowResearchPointerState struct {
	exists       bool
	sameArtifact bool
	raw          []byte
	championRaw  []byte
}

func (controller *shadowResearchController) candidateRotationState(
	artifactPath, artifactSHA256, candidatePolicySHA256 string, now time.Time,
) (shadowResearchPointerState, error) {
	championRaw, err := securefile.ReadPrivate(controller.championPointer, shadowCandidatePointerBytes)
	if err != nil {
		return shadowResearchPointerState{}, errors.New("could not read the paper champion pointer")
	}
	state := shadowResearchPointerState{championRaw: championRaw}
	raw, err := securefile.ReadPrivate(controller.challengerPointer, shadowCandidatePointerBytes)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return shadowResearchPointerState{}, errors.New("could not read the paper challenger pointer")
	}
	if err == nil {
		state.exists, state.raw = true, raw
		_, activePath, activeSHA256, selection, err := loadBoundShadowCandidateSelection(
			controller.challengerPointer, controller.basePolicy,
		)
		if err != nil || selection.SelectedAt == nil || selection.EligibleFrom == nil ||
			selection.ChallengeDays != controller.challengeDays ||
			selection.ChallengeGateVersion != shadowChallengeGateVersion ||
			filepath.Dir(activePath) != controller.candidateDir {
			return shadowResearchPointerState{}, errors.New("paper challenger pointer is invalid")
		}
		if activePath == artifactPath && activeSHA256 == artifactSHA256 {
			state.sameArtifact = true
			return state, nil
		}
		status, err := controller.challengeStatus(now)
		if err != nil {
			return shadowResearchPointerState{}, err
		}
		if err := validateShadowResearchRotationStatus(status.Status); err != nil {
			return shadowResearchPointerState{}, err
		}
	}
	champion, _, _, err := loadBoundSelectedShadowCandidate(
		controller.championPointer, controller.basePolicy,
	)
	championCurrent, currentErr := securefile.ReadPrivate(
		controller.championPointer, shadowCandidatePointerBytes,
	)
	if err != nil || currentErr != nil || !bytes.Equal(championRaw, championCurrent) {
		return shadowResearchPointerState{}, errors.New("paper champion pointer changed while research was running")
	}
	if champion.CandidatePolicySHA256 == candidatePolicySHA256 {
		return shadowResearchPointerState{}, errors.New("paper challenger policy is identical to the current champion")
	}
	return state, nil
}

func validateShadowResearchRotationStatus(status string) error {
	if status == "challenger_not_qualified" || status == "challenger_selected_as_paper_champion" {
		return nil
	}
	return fmt.Errorf("active paper challenger retained: %s", status)
}

func (controller *shadowResearchController) verifyChallengerPointerState(
	state shadowResearchPointerState,
) error {
	champion, err := securefile.ReadPrivate(controller.championPointer, shadowCandidatePointerBytes)
	if err != nil || !bytes.Equal(state.championRaw, champion) {
		return errors.New("paper champion pointer changed while research was running")
	}
	current, err := securefile.ReadPrivate(controller.challengerPointer, shadowCandidatePointerBytes)
	if !state.exists && errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || !state.exists || !bytes.Equal(state.raw, current) {
		return errors.New("paper challenger pointer changed while research was running")
	}
	return nil
}

func (controller *shadowResearchController) challengeStatus(
	now time.Time,
) (shadowResearchChallengeStatus, error) {
	if _, err := securefile.ReadPrivate(controller.challengerPointer, shadowCandidatePointerBytes); errors.Is(err, os.ErrNotExist) {
		return shadowResearchChallengeStatus{
			Status: "no_active_challenger", PaperOnly: true,
		}, nil
	} else if err != nil {
		return shadowResearchChallengeStatus{}, errors.New("could not read the paper challenger pointer")
	}
	active, activePath, activeSHA256, selection, err := loadBoundShadowCandidateSelection(
		controller.challengerPointer, controller.basePolicy,
	)
	if err != nil || selection.SelectedAt == nil || selection.EligibleFrom == nil ||
		selection.ChallengeDays != controller.challengeDays ||
		selection.ChallengeGateVersion != shadowChallengeGateVersion ||
		filepath.Dir(activePath) != controller.candidateDir {
		return shadowResearchChallengeStatus{}, errors.New("paper challenger pointer is invalid")
	}
	champion, _, championSHA256, err := loadBoundSelectedShadowCandidate(
		controller.championPointer, controller.basePolicy,
	)
	if err != nil {
		return shadowResearchChallengeStatus{}, errors.New("paper champion pointer is invalid")
	}
	if championSHA256 == activeSHA256 {
		return shadowResearchChallengeStatus{
			Status: "challenger_selected_as_paper_champion", PaperOnly: true,
			ActiveArtifact: filepath.Base(activePath), ActiveArtifactSHA256: activeSHA256,
			ActivePolicySHA256: active.CandidatePolicySHA256,
		}, nil
	}
	if champion.CandidatePolicySHA256 == active.CandidatePolicySHA256 {
		return shadowResearchChallengeStatus{}, errors.New(
			"paper champion policy matches the challenger but its artifact digest differs",
		)
	}
	status := shadowResearchChallengeStatus{
		Status: "challenger_evidence_pending", PaperOnly: true,
		ActiveArtifact: filepath.Base(activePath), ActiveArtifactSHA256: activeSHA256,
		ActivePolicySHA256: active.CandidatePolicySHA256,
		Reasons:            []string{"paired_complete_days_unavailable"},
	}
	result, err := evaluateShadowChallenge(
		controller.basePolicy, controller.championPointer, activePath,
		controller.championRoot, controller.challengerRoot,
		selection.ChallengeDays, now, selection.EligibleFrom.UTC(),
	)
	if errors.Is(err, errShadowChallengeEvidencePending) {
		return status, nil
	}
	if err != nil {
		return shadowResearchChallengeStatus{}, err
	}
	status.Status = result.Status
	status.EligibleForPaperSelection = result.EligibleForPaperSelection
	status.CompleteDays = result.CompleteDays
	status.ChallengerFullRoundTrips = result.ChallengerFullRoundTrips
	status.RequiredFullRoundTrips = result.RequiredFullRoundTrips
	status.ChallengerDailyWins = result.ChallengerDailyWins
	status.RequiredDailyWins = result.RequiredDailyWins
	status.AggregateAdvantageMicros = result.AggregateAdvantageMicros
	status.RequiredAggregateAdvantageMicros = result.RequiredAggregateAdvantageMicros
	status.Reasons = result.Reasons
	return status, nil
}

func (controller *shadowResearchController) ensureCandidateArtifact(path string, encoded []byte) error {
	if existing, err := securefile.ReadPrivate(path, maxInputBytes); err == nil {
		if !bytes.Equal(existing, encoded) {
			return errors.New("paper challenger digest collision")
		}
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := validatePrivateDirectory(controller.candidateDir); err != nil {
		return err
	}
	directory, err := os.Open(controller.candidateDir)
	if err != nil {
		return err
	}
	entries, readErr := directory.ReadDir(controller.maxCandidates)
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if len(entries) >= controller.maxCandidates {
		return errors.New("paper challenger quota reached")
	}
	if err := securefile.CreatePrivate(path, encoded, maxInputBytes); err == nil {
		return nil
	}
	// Another creator may have won the content-addressed path race. The exact
	// bytes are safe to reuse; any other object fails closed.
	existing, err := securefile.ReadPrivate(path, maxInputBytes)
	if err != nil || !bytes.Equal(existing, encoded) {
		return errors.New("could not create paper challenger")
	}
	return nil
}

func (input shadowResearchCandidateInput) validate(now time.Time) error {
	if err := input.Hypothesis.validate(); err != nil {
		return err
	}
	for _, source := range input.Hypothesis.Sources {
		if source.ObservedAt.After(now) {
			return errors.New("paper hypothesis source observation cannot be in the future")
		}
	}
	trainAt, trainErr := time.Parse("2006-01-02", input.TrainDay)
	validationAt, validationErr := time.Parse("2006-01-02", input.ValidationDay)
	today := time.Date(now.UTC().Year(), now.UTC().Month(), now.UTC().Day(), 0, 0, 0, 0, time.UTC)
	wantValidation := today.AddDate(0, 0, -1)
	wantTrain := today.AddDate(0, 0, -2)
	if trainErr != nil || validationErr != nil ||
		!trainAt.Equal(wantTrain) || !validationAt.Equal(wantValidation) {
		return errors.New("paper hypothesis needs yesterday and the day before as its eight-day walk-forward anchor")
	}
	return nil
}

func (hypothesis shadowPaperHypothesis) validate() error {
	if hypothesis.Version != shadowPaperHypothesisVersion ||
		hypothesis.Status != "paper_hypothesis" || hypothesis.Authorized ||
		hypothesis.Promotable || !hypothesis.PaperOnly {
		return errors.New("paper hypothesis safety markers are invalid")
	}
	if !boundedResearchText(hypothesis.Thesis, 1000) ||
		len(hypothesis.Sources) == 0 || len(hypothesis.Sources) > shadowHypothesisMaxSources {
		return errors.New("paper hypothesis thesis or source count is invalid")
	}
	seen := make(map[string]struct{}, len(hypothesis.Sources))
	for _, source := range hypothesis.Sources {
		parsed, err := url.ParseRequestURI(source.URL)
		if err != nil || len(source.URL) > 2048 || parsed.Scheme != "https" ||
			parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" ||
			!allowedPaperResearchSource(parsed) || source.ObservedAt.IsZero() ||
			!boundedResearchText(source.Summary, 500) {
			return errors.New("paper hypothesis source is invalid")
		}
		if _, exists := seen[source.URL]; exists {
			return errors.New("paper hypothesis sources must be distinct")
		}
		seen[source.URL] = struct{}{}
	}
	return nil
}

func allowedPaperResearchSource(source *url.URL) bool {
	host := strings.ToLower(source.Hostname())
	switch host {
	case "solana.com", "status.solana.com", "anza.xyz", "developers.jup.ag",
		"status.jup.ag", "api.jup.ag", "docs.pyth.network", "status.pyth.network",
		"docs.kraken.com", "status.kraken.com", "api.kraken.com", "helius.dev",
		"www.helius.dev", "docs.helius.dev", "helius.statuspage.io", "docs.jito.wtf":
		return true
	case "github.com":
		for _, prefix := range []string{
			"/anza-xyz/", "/solana-foundation/", "/jup-ag/", "/pyth-network/", "/krakenfx/",
		} {
			if strings.HasPrefix(strings.ToLower(source.EscapedPath()), prefix) {
				return true
			}
		}
	}
	return false
}

func boundedResearchText(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return false
		}
	}
	return true
}
