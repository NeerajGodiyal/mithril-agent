package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/securefile"
	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/marketadmission"
	"github.com/Overclock-Validator/mithril-agent/paperstatus"
	"github.com/Overclock-Validator/mithril-agent/pricesource"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/shadow"
	"github.com/Overclock-Validator/mithril-agent/swapbuilder"
)

// Shadow mode is safe to point at Mainnet for a structural reason: the shadow
// package has no signer, no submitter, and no way to name a key. This command
// wires it to real data and nothing else.
//
// Each UTC day is a reset-daily operational canary. The books open at the
// day's first observed price and close with that day's report, so gains are not
// compounded across days. Each report compares the rule with holding from the
// same opening mark; separate days are not independent statistical trials.
const shadowRunUsage = `Usage: mithril-agent shadow run --policy PATH --dir PATH [options]

Watches a live market and records what the rule would have done. Nothing is
signed and nothing is submitted; no wallet signing key is loaded at any point.

  --policy PATH         shadow policy JSON
  --dir PATH            directory for the daily journals and reports
  --candidate-pointer PATH
                        selected paper candidate; checked only at startup and
                        UTC-day boundaries
  --portfolio PATH      private shared paper-capital manifest
  --portfolio-book ID   book in that manifest bound to this base policy
  --admission-artifact PATH
                        qualified market evidence required by WIF/USDC policies
  --admission-journal PATH
                        exact journal bound by that artifact
  --provisional-artifact PATH
                        six-hour paper-only evidence for a candidate market
  --provisional-journal PATH
                        exact journal bound by that checkpoint
  --paper-check-artifact PATH
                        passing short paper-check required by provisional policy
  --alert-status PATH   private bounded paper-event snapshot for Telegram
  --quote-provider NAME optional compatibility check; must match the policy
  --node-command PATH   Node.js runtime for the read-only quote adapter
  --quote-script PATH   read-only quote adapter
  --pool ADDRESS        optional compatibility check; must match the policy
  --input-mint ADDRESS  optional compatibility check; must match the policy
  --output-mint ADDRESS optional compatibility check; must match the policy
  --once                take a single observation and stop

Endpoints come from the environment and are never printed, logged, or written
to the journal:

  MITHRIL_AGENT_SHADOW_RPC_URL   price reads; https, or http on a literal
                                 loopback IP so it can read your own node
  MITHRIL_AGENT_QUOTE_RPC_URL    the Orca quote adapter; https only
  MITHRIL_AGENT_JUPITER_API_KEY  optional for tests; Jupiter recommends an API
                                 key for continuous production use
  MITHRIL_AGENT_KRAKEN_RATE_STATE
                                 private host-shared request gate for multiple
                                 supervised paper observers`

const (
	shadowEndpointEnvironment = "MITHRIL_AGENT_SHADOW_RPC_URL"
	quoteEndpointEnvironment  = "MITHRIL_AGENT_QUOTE_RPC_URL"
	jupiterAPIKeyEnvironment  = "MITHRIL_AGENT_JUPITER_API_KEY"
	mainnetUSDCMint           = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"
)

var errProvisionalPaperComplete = errors.New("provisional paper experiment completed")

type shadowRunOptions struct {
	policyPath          string
	directory           string
	candidatePointer    string
	portfolioPath       string
	portfolioBook       string
	admissionArtifact   string
	admissionJournal    string
	provisionalArtifact string
	provisionalJournal  string
	paperCheckArtifact  string
	alertStatus         string
	quoteSource         string
	nodeCommand         string
	quoteScript         string
	pool                string
	inputMint           string
	outputMint          string
	once                bool
}

func runShadowRun(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("shadow run", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	options := shadowRunOptions{}
	flags.StringVar(&options.policyPath, "policy", "", "shadow policy JSON")
	flags.StringVar(&options.directory, "dir", "", "journal and report directory")
	flags.StringVar(&options.candidatePointer, "candidate-pointer", "", "selected paper candidate")
	flags.StringVar(&options.portfolioPath, "portfolio", "", "shared paper-capital manifest")
	flags.StringVar(&options.portfolioBook, "portfolio-book", "", "paper-capital book ID")
	flags.StringVar(&options.admissionArtifact, "admission-artifact", "", "qualified market evidence")
	flags.StringVar(&options.admissionJournal, "admission-journal", "", "market evidence journal")
	flags.StringVar(&options.provisionalArtifact, "provisional-artifact", "", "six-hour paper-only market evidence")
	flags.StringVar(&options.provisionalJournal, "provisional-journal", "", "provisional market evidence journal")
	flags.StringVar(&options.paperCheckArtifact, "paper-check-artifact", "", "passing short paper-check result")
	flags.StringVar(&options.alertStatus, "alert-status", "", "private paper-event snapshot")
	flags.StringVar(&options.quoteSource, "quote-provider", "", "must match policy when set")
	flags.StringVar(&options.nodeCommand, "node-command", "", "Node.js runtime")
	flags.StringVar(&options.quoteScript, "quote-script", "", "read-only quote adapter")
	flags.StringVar(&options.pool, "pool", "", "pool address to quote against")
	flags.StringVar(&options.inputMint, "input-mint", "", "mint being spent")
	flags.StringVar(&options.outputMint, "output-mint", "", "mint being received")
	flags.BoolVar(&options.once, "once", false, "take one observation and stop")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, shadowRunUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("shadow run takes no positional arguments")
	}
	policy, err := loadActiveShadowPolicy(options.policyPath)
	if err != nil {
		return err
	}
	run, err := openShadowRun(ctx, policy, options)
	if err != nil {
		return err
	}
	defer func() { _ = run.roll.Close() }()
	if err := notifyShadowRunReady(); err != nil {
		return err
	}
	return run.drive(ctx, options.once, output)
}

func notifyShadowRunReady() error {
	path := os.Getenv("NOTIFY_SOCKET")
	if path == "" {
		return nil
	}
	if path[0] == '@' {
		path = "\x00" + path[1:]
	}
	connection, err := net.DialUnix(
		"unixgram", nil, &net.UnixAddr{Name: path, Net: "unixgram"},
	)
	if err != nil {
		return errors.New("connect to systemd notification socket")
	}
	defer connection.Close()
	if _, err := connection.Write([]byte("READY=1")); err != nil {
		return errors.New("notify systemd that paper runner is ready")
	}
	return nil
}

// loadShadowPolicy reads the policy strictly: an unknown field is a refusal,
// not something to ignore. A shadow policy has no field that could name a
// wallet signing key, so a configuration that tries to supply one cannot be
// loaded at all.
func loadShadowPolicy(path string) (shadow.Policy, error) {
	if path == "" {
		return shadow.Policy{}, errors.New("this command requires --policy PATH " +
			"(create one with: mithril-agent shadow policy --out PATH ...)")
	}
	clean, err := cleanExistingPath(path)
	if err != nil {
		return shadow.Policy{}, err
	}
	var policy shadow.Policy
	if err := readStrictJSON(clean, &policy); err != nil {
		return shadow.Policy{}, err
	}
	if err := policy.Validate(); err != nil {
		return shadow.Policy{}, err
	}
	return policy, nil
}

func loadActiveShadowPolicy(path string) (shadow.Policy, error) {
	policy, err := loadShadowPolicy(path)
	if err != nil {
		return shadow.Policy{}, err
	}
	if err := validateActiveShadowPolicy(policy); err != nil {
		return shadow.Policy{}, err
	}
	return policy, nil
}

func validateActiveShadowPolicy(policy shadow.Policy) error {
	if policy.Trigger.SecondarySourceSHA256 == pricesource.CoinbaseIdentitySHA256() ||
		policy.ReturnTrigger != nil &&
			policy.ReturnTrigger.SecondarySourceSHA256 == pricesource.CoinbaseIdentitySHA256() {
		return errors.New("legacy Coinbase-bound shadow policy is unsupported for active use; regenerate the policy with the current market sources")
	}
	return nil
}

// shadowRun holds the wiring for the whole run. A day boundary rebuilds the
// runner from exactly these dependencies, so crossing midnight cannot silently
// change what is being read or where it is recorded.
type shadowRun struct {
	basePolicy        shadow.Policy
	policy            shadow.Policy
	journalRoot       string
	candidatePointer  string
	policySHA256      string
	primary           shadow.PriceReader
	secondary         shadow.PriceReader
	quotePrimary      shadow.PriceReader
	quoteSecondary    shadow.PriceReader
	nativePrimary     shadow.PriceReader
	nativeSecondary   shadow.PriceReader
	quoter            shadow.Quoter
	roll              *dailyJournal
	runner            *shadow.Runner
	alerts            *paperstatus.Writer
	reconcilingAlerts bool
	// activationSequence counts durable period-close records in today's
	// journal. A crash restart reuses the same sequence and stays deduplicated;
	// a restart after a clean stop announces that the strategy resumed.
	activationSequence uint64
	// lastPrice is the most recent price actually observed. The report closes
	// on it rather than on a cost basis, which would not be a market price.
	lastPrice                   uint64
	consecutiveUnavailable      uint8
	dataUnavailable             bool
	admissionThrough            time.Time
	portfolioMaxSOL             uint64
	portfolioBound              bool
	portfolioPaperCapitalMicros uint64
	portfolioInstructionSHA256  string
}

func openShadowRun(ctx context.Context, policy shadow.Policy, options shadowRunOptions) (*shadowRun, error) {
	now := time.Now().UTC()
	basePolicy := policy
	var candidateInstructionSHA256 string
	var candidatePaperCapitalMicros uint64
	policyFingerprint, err := policy.Fingerprint()
	if err != nil {
		return nil, err
	}
	if options.candidatePointer != "" {
		candidate, _, err := loadSelectedShadowCandidate(options.candidatePointer, policy)
		if err != nil {
			return nil, err
		}
		policy, policyFingerprint = candidate.Policy, candidate.CandidatePolicySHA256
		if candidate.Experiment != nil {
			candidateInstructionSHA256 = candidate.Experiment.InstructionSHA256
			candidatePaperCapitalMicros = candidate.Experiment.Instruction.PaperCapitalMicros
		}
		policy, policyFingerprint, err = resolveStartupShadowPolicy(
			basePolicy, policy, policyFingerprint, options.directory, now,
		)
		if err != nil {
			return nil, err
		}
	}
	if err := validateActiveShadowPolicy(policy); err != nil {
		return nil, err
	}
	if (options.portfolioPath == "") != (options.portfolioBook == "") {
		return nil, errors.New("shadow run requires --portfolio and --portfolio-book together")
	}
	portfolioMaxSOL := uint64(0)
	portfolioInstructionSHA256 := ""
	portfolioPaperCapitalMicros := uint64(0)
	if options.portfolioPath != "" {
		portfolioMaxSOL, portfolioInstructionSHA256, portfolioPaperCapitalMicros, err = loadShadowPortfolioBindingForBook(
			options.portfolioPath, options.portfolioBook, options.policyPath, basePolicy,
		)
		if err != nil {
			return nil, err
		}
		if err := validateShadowPortfolioCandidateBinding(
			options.candidatePointer != "", portfolioInstructionSHA256,
			portfolioPaperCapitalMicros, candidateInstructionSHA256,
			candidatePaperCapitalMicros,
		); err != nil {
			return nil, err
		}
	} else if policy.Version == shadow.AdmittedVersion {
		return nil, errors.New("admitted market paper runs require a shared portfolio manifest")
	}
	var admissionCandidate *marketadmission.Candidate
	var admissionThrough time.Time
	qualifiedEvidence := options.admissionArtifact != "" || options.admissionJournal != ""
	provisionalEvidence := options.provisionalArtifact != "" || options.provisionalJournal != ""
	paperCheckEvidence := options.paperCheckArtifact != ""
	if qualifiedEvidence && provisionalEvidence {
		return nil, errors.New("choose qualified or provisional market evidence, not both")
	}
	if policy.Version == shadow.AdmittedVersion {
		if policy.MarketEvidenceClass == shadow.MarketEvidenceDevelopmentProvisional {
			if qualifiedEvidence {
				return nil, errors.New("development paper policy requires provisional market evidence")
			}
			artifact, err := loadProvisionalMarketAdmission(
				options.provisionalArtifact, options.provisionalJournal, now,
			)
			if err != nil {
				return nil, err
			}
			if !provisionalPolicyMatchesArtifact(policy, artifact) {
				return nil, errors.New("provisional market evidence does not match the active policy")
			}
			if !paperCheckEvidence {
				return nil, errors.New("development paper policy requires a passing paper-check artifact")
			}
			if _, err := loadMarketPaperCheckResult(
				options.paperCheckArtifact, policy, artifact, options.provisionalJournal, now,
			); err != nil {
				return nil, err
			}
			admissionCandidate, admissionThrough = &artifact.Candidate, artifact.Through
		} else {
			if provisionalEvidence || paperCheckEvidence {
				return nil, errors.New("qualified paper policy requires long-run market evidence")
			}
			artifact, err := loadQualifiedMarketAdmission(
				options.admissionArtifact, options.admissionJournal, now,
			)
			if err != nil {
				return nil, err
			}
			if !admittedPolicyMatchesArtifact(policy, artifact) {
				return nil, errors.New("market admission evidence does not match the active policy")
			}
			admissionCandidate, admissionThrough = &artifact.Candidate, artifact.Through
		}
	} else if qualifiedEvidence || provisionalEvidence || paperCheckEvidence {
		return nil, errors.New("market admission flags require an admitted candidate-market policy")
	}
	endpoint := os.Getenv(shadowEndpointEnvironment)
	if err := validateShadowEndpoint(endpoint); err != nil {
		return nil, err
	}
	reader := publicAccountReader(endpoint)
	var primary shadow.PriceReader
	var secondary shadow.PriceReader
	if admissionCandidate != nil {
		primary, err = pricesource.NewPythPushFromSpec(
			reader, time.Now, admissionCandidate.Pyth,
		)
		if err != nil {
			return nil, err
		}
		secondary, err = pricesource.NewKrakenFromSpec(nil, admissionCandidate.Kraken)
	} else if policy.Market == shadow.MarketJUPUSDC {
		primary, err = pricesource.NewPythPushJUP(reader, time.Now)
		secondary = pricesource.NewKrakenJUP(nil)
	} else {
		primary, err = pricesource.NewPythPush(reader, time.Now)
		secondary = pricesource.NewKrakenSOL(nil)
	}
	if err != nil {
		return nil, err
	}
	var quotePrimary shadow.PriceReader
	var quoteSecondary shadow.PriceReader
	if policy.QuotePeg != nil {
		quotePrimary, err = pricesource.NewPythPushUSDC(reader, time.Now)
		if err != nil {
			return nil, err
		}
		quoteSecondary = pricesource.NewKraken(nil)
	}
	var nativePrimary shadow.PriceReader
	var nativeSecondary shadow.PriceReader
	if policy.NativeFeePrice != nil {
		nativePrimary, err = pricesource.NewPythPush(reader, time.Now)
		if err != nil {
			return nil, err
		}
		nativeSecondary = pricesource.NewKrakenSOL(nil)
	}
	if portfolioMaxSOL != 0 {
		ceilingPolicy := policy.Trigger
		ceilingPrimary, ceilingSecondary := primary, secondary
		if policy.NativeFeePrice != nil {
			ceilingPolicy = *policy.NativeFeePrice
			ceilingPrimary, ceilingSecondary = nativePrimary, nativeSecondary
		}
		if err := validateShadowPortfolioSOLPrice(
			ctx, ceilingPolicy, ceilingPrimary, ceilingSecondary, portfolioMaxSOL,
		); err != nil {
			return nil, err
		}
	}
	quoter, err := newShadowQuoter(policy, options)
	if err != nil {
		return nil, err
	}
	var alerts *paperstatus.Writer
	if options.alertStatus != "" {
		alerts, err = paperstatus.OpenWriter(options.alertStatus)
		if err != nil {
			return nil, err
		}
	}
	journalDirectory := options.directory
	if options.candidatePointer != "" {
		journalDirectory = filepath.Join(options.directory, policyFingerprint)
	}
	roll, err := newDailyJournal(journalDirectory)
	if err != nil {
		return nil, err
	}
	if options.candidatePointer != "" {
		if err := ensureShadowPolicySnapshot(journalDirectory, policy); err != nil {
			roll.Close()
			return nil, err
		}
	}
	run := &shadowRun{
		basePolicy: basePolicy, policy: policy, journalRoot: options.directory,
		candidatePointer: options.candidatePointer,
		policySHA256:     policyFingerprint,
		primary:          primary, secondary: secondary,
		quotePrimary: quotePrimary, quoteSecondary: quoteSecondary,
		nativePrimary: nativePrimary, nativeSecondary: nativeSecondary,
		quoter: quoter, roll: roll, alerts: alerts,
		portfolioMaxSOL:             portfolioMaxSOL,
		portfolioBound:              options.portfolioPath != "",
		portfolioPaperCapitalMicros: portfolioPaperCapitalMicros,
		portfolioInstructionSHA256:  portfolioInstructionSHA256,
	}
	if !admissionThrough.IsZero() {
		run.admissionThrough = admissionThrough
	}
	if err := run.reconcileMissingShadowReports(); err != nil {
		roll.Close()
		return nil, err
	}
	if err := roll.openFor(now); err != nil {
		roll.Close()
		return nil, err
	}
	records := roll.Records()
	if len(records) == 0 {
		run.runner, err = run.newRunner()
	} else {
		run.runner, run.lastPrice, err = run.resumeRunner(policy, roll)
	}
	if err != nil {
		roll.Close()
		return nil, err
	}
	if len(records) != 0 {
		ticks, decodeErr := shadowTicksFrom(records, policy, true)
		if decodeErr != nil {
			roll.Close()
			return nil, decodeErr
		}
		if err := run.reconcileAlertTicks(ticks); err != nil {
			roll.Close()
			return nil, err
		}
		run.activationSequence = periodCloseCount(ticks)
	}
	if err := run.reconcileStoredShadowReports(); err != nil {
		roll.Close()
		return nil, err
	}
	if err := run.alertStrategy(now, paperstatus.KindStrategyActive); err != nil {
		roll.Close()
		return nil, err
	}
	return run, nil
}

func validateShadowPortfolioSOLPrice(
	ctx context.Context,
	policy pricetrigger.Policy,
	primary, secondary shadow.PriceReader,
	maximum uint64,
) error {
	left, err := primary.Latest(ctx, policy.Feed)
	if err != nil {
		return errors.New("paper portfolio SOL/USD evidence is unavailable")
	}
	right, err := secondary.Latest(ctx, policy.Feed)
	if err != nil {
		return errors.New("paper portfolio SOL/USD evidence is unavailable")
	}
	policy.Direction = pricetrigger.BuyAtOrBelow
	policy.ThresholdMicros = pricetrigger.MaxPriceMicros
	evidence, err := pricetrigger.Evaluate(policy, left, right, time.Now().UTC())
	if err != nil || evidence.ConservativePrice > maximum {
		return errors.New("paper portfolio SOL/USD evidence exceeds its planning ceiling")
	}
	return nil
}

func admittedPolicyMatchesArtifact(policy shadow.Policy, artifact marketadmission.Artifact) bool {
	return policy.MarketEvidenceClass != shadow.MarketEvidenceDevelopmentProvisional &&
		marketPolicyMatchesEvidence(
			policy, artifact.Candidate, artifact.Observe, artifact.ContentSHA256, artifact.Thresholds,
		)
}

func provisionalPolicyMatchesArtifact(
	policy shadow.Policy,
	artifact marketadmission.ProvisionalArtifact,
) bool {
	return policy.MarketEvidenceClass == shadow.MarketEvidenceDevelopmentProvisional &&
		marketPolicyMatchesEvidence(
			policy, artifact.Candidate, artifact.Observe, artifact.ContentSHA256, artifact.Thresholds,
		)
}

func marketPolicyMatchesEvidence(
	policy shadow.Policy,
	candidate marketadmission.Candidate,
	observe, digest string,
	limits marketadmission.Thresholds,
) bool {
	primary, primaryErr := candidate.Pyth.IdentitySHA256()
	secondary, secondaryErr := candidate.Kraken.IdentitySHA256()
	if primaryErr != nil || secondaryErr != nil || policy.Adaptive == nil ||
		policy.ReturnTrigger == nil || policy.NativeFeePrice == nil || policy.QuotePeg == nil ||
		candidate.Market != policy.Market ||
		digest != policy.MarketEvidenceSHA256 ||
		observe != policy.Observe ||
		candidate.Pyth.Feed != policy.Trigger.Feed ||
		primary != policy.Trigger.PrimarySourceSHA256 ||
		secondary != policy.Trigger.SecondarySourceSHA256 ||
		candidate.QuoteMint != policy.QuoteRoute.InputMint ||
		candidate.BaseMint != policy.QuoteRoute.OutputMint ||
		candidate.QuoteNotionalUSDC != policy.InputAmount ||
		candidate.QuoteSlippageBPS != policy.SlippageBPS ||
		policy.Adaptive.MaxQuoteImpactBPS > limits.MaximumQuoteImpactBPS {
		return false
	}
	return triggerWithinAdmission(policy.Trigger, limits) &&
		triggerWithinAdmission(*policy.ReturnTrigger, limits) &&
		triggerWithinAdmission(*policy.NativeFeePrice, limits) &&
		bandWithinAdmission(*policy.QuotePeg, limits)
}

func triggerWithinAdmission(
	policy pricetrigger.Policy,
	limits marketadmission.Thresholds,
) bool {
	return policy.MaxAgeSeconds <= uint64(limits.MaximumSourceAgeSeconds) &&
		policy.MaxSourceSkewSeconds <= uint64(limits.MaximumSourceSkewSeconds) &&
		policy.MaxDeviationBPS <= limits.MaximumSourceDeviationBPS &&
		policy.MaxConfidenceBPS <= limits.MaximumConfidenceBPS
}

func bandWithinAdmission(
	policy pricetrigger.BandPolicy,
	limits marketadmission.Thresholds,
) bool {
	return policy.MaxAgeSeconds <= uint64(limits.MaximumSourceAgeSeconds) &&
		policy.MaxSourceSkewSeconds <= uint64(limits.MaximumSourceSkewSeconds) &&
		policy.MaxDeviationBPS <= limits.MaximumSourceDeviationBPS &&
		policy.MaxConfidenceBPS <= limits.MaximumConfidenceBPS
}

// resolveStartupShadowPolicy keeps a mid-day restart on the policy already
// pinned by today's journal. A new pointer becomes active only when no candidate
// has begun the current UTC day, matching the running process's boundary rule.
func resolveStartupShadowPolicy(
	base, selected shadow.Policy,
	selectedSHA256, root string,
	now time.Time,
) (shadow.Policy, string, error) {
	pattern := filepath.Join(
		root, strings.Repeat("?", 64), "shadow-"+dayKey(now)+".jsonl",
	)
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return shadow.Policy{}, "", errors.New("could not inspect current shadow candidate journals")
	}
	if len(paths) == 0 {
		return selected, selectedSHA256, nil
	}
	if len(paths) != 1 {
		return shadow.Policy{}, "", errors.New("multiple shadow candidates already have journals for this UTC day")
	}
	journalInfo, err := os.Lstat(paths[0])
	if err != nil || !journalInfo.Mode().IsRegular() || journalInfo.Mode()&os.ModeSymlink != 0 {
		return shadow.Policy{}, "", errors.New("current shadow candidate journal is not a regular file")
	}
	directory := filepath.Dir(paths[0])
	if err := validatePrivateDirectory(directory); err != nil {
		return shadow.Policy{}, "", errors.New("current shadow candidate directory is not private")
	}
	stored, err := loadActiveShadowPolicy(filepath.Join(directory, "policy.json"))
	if err != nil {
		return shadow.Policy{}, "", errors.New("current shadow candidate policy snapshot is invalid")
	}
	if err := validateShadowSearchLineage(base, stored); err != nil {
		return shadow.Policy{}, "", err
	}
	fingerprint, err := stored.Fingerprint()
	if err != nil || fingerprint != filepath.Base(directory) {
		return shadow.Policy{}, "", errors.New("current shadow candidate policy does not match its directory")
	}
	return stored, fingerprint, nil
}

// validateShadowEndpoint requires TLS for anything off-box, and permits plain
// HTTP only to a literal loopback IP.
//
// The reason for the exception is practical rather than lax: reading from a
// node on the same machine cannot be intercepted, and the operator's own
// verifying node is the whole point of this system. Refusing it would push
// people toward a public endpoint instead, which is strictly worse evidence.
func validateShadowEndpoint(endpoint string) error {
	// The endpoint itself never appears in an error: it may carry a key.
	refuse := errors.New(shadowEndpointEnvironment +
		" must be an https endpoint, or http on a literal loopback IP")
	if validatePublicAccountEndpoint(endpoint) != nil {
		return refuse
	}
	return nil
}

func validatePublicAccountEndpoint(endpoint string) error {
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("public account RPC endpoint is invalid")
	}
	switch parsed.Scheme {
	case "https":
		return nil
	case "http":
		if isLoopbackHost(parsed.Hostname()) {
			return nil
		}
	}
	return errors.New("public account RPC endpoint is invalid")
}

func isLoopbackHost(host string) bool {
	address := net.ParseIP(host)
	return address != nil && address.IsLoopback()
}

func (s *shadowRun) newRunner() (*shadow.Runner, error) {
	return s.newRunnerFor(s.policy, s.roll)
}

func (s *shadowRun) newRunnerFor(policy shadow.Policy, roll *dailyJournal) (*shadow.Runner, error) {
	return shadow.NewRunner(
		policy, s.primary, s.secondary, s.quoter, roll, s.quoteReadersFor(policy)...,
	)
}

func (s *shadowRun) quoteReadersFor(policy shadow.Policy) []shadow.PriceReader {
	var readers []shadow.PriceReader
	if policy.QuotePeg != nil {
		readers = append(readers, s.quotePrimary, s.quoteSecondary)
	}
	if policy.NativeFeePrice != nil {
		readers = append(readers, s.nativePrimary, s.nativeSecondary)
	}
	return readers
}

func (s *shadowRun) resumeRunner(
	policy shadow.Policy, roll *dailyJournal,
) (*shadow.Runner, uint64, error) {
	if len(roll.Records()) == 0 {
		runner, err := s.newRunnerFor(policy, roll)
		return runner, 0, err
	}
	ticks, err := shadowTicksFrom(roll.Records(), policy, true)
	if err != nil {
		return nil, 0, err
	}
	runner, err := shadow.ResumeRunner(
		policy, s.primary, s.secondary, s.quoter, roll, ticks,
		s.quoteReadersFor(policy)...,
	)
	if err != nil {
		return nil, 0, err
	}
	return runner, lastShadowPrice(ticks), nil
}

func lastShadowPrice(ticks []shadow.Tick) uint64 {
	for index := len(ticks) - 1; index >= 0; index-- {
		if ticks[index].PriceMicros != 0 {
			return ticks[index].PriceMicros
		}
	}
	return 0
}

func ensureShadowPolicySnapshot(directory string, policy shadow.Policy) error {
	path := filepath.Join(directory, "policy.json")
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		encoded, marshalErr := json.MarshalIndent(policy, "", "  ")
		if marshalErr != nil {
			return marshalErr
		}
		if err := securefile.CreatePrivate(path, append(encoded, '\n'), maxInputBytes); err != nil {
			return errors.New("could not write the selected shadow policy snapshot")
		}
		return nil
	} else if err != nil {
		return errors.New("could not inspect the selected shadow policy snapshot")
	}
	stored, err := loadShadowPolicy(path)
	if err != nil {
		return errors.New("could not verify the selected shadow policy snapshot")
	}
	want, err := policy.Fingerprint()
	if err != nil {
		return err
	}
	got, err := stored.Fingerprint()
	if err != nil || got != want {
		return errors.New("selected shadow policy snapshot does not match its directory")
	}
	return nil
}

// refreshSelectedCandidate is called only between complete UTC trials. It
// refuses an invalid pointer and keeps each policy's evidence in a separate
// fingerprinted directory, so a dynamic selection cannot rewrite or mix the
// journal that justified it.
func (s *shadowRun) refreshSelectedCandidate(now time.Time) error {
	if s.candidatePointer == "" {
		return nil
	}
	candidate, _, err := loadSelectedShadowCandidate(s.candidatePointer, s.basePolicy)
	if err != nil {
		return err
	}
	if s.portfolioBound {
		candidateInstructionSHA256 := ""
		candidatePaperCapitalMicros := uint64(0)
		if candidate.Experiment != nil {
			candidateInstructionSHA256 = candidate.Experiment.InstructionSHA256
			candidatePaperCapitalMicros = candidate.Experiment.Instruction.PaperCapitalMicros
		}
		if err := validateShadowPortfolioCandidateBinding(
			true, s.portfolioInstructionSHA256, s.portfolioPaperCapitalMicros,
			candidateInstructionSHA256, candidatePaperCapitalMicros,
		); err != nil {
			return err
		}
	}
	if err := validateActiveShadowPolicy(candidate.Policy); err != nil {
		return err
	}
	if candidate.CandidatePolicySHA256 == s.policySHA256 {
		return ensureShadowPolicySnapshot(s.roll.directory, candidate.Policy)
	}
	roll, err := newDailyJournal(filepath.Join(s.journalRoot, candidate.CandidatePolicySHA256))
	if err != nil {
		return err
	}
	if err := ensureShadowPolicySnapshot(roll.directory, candidate.Policy); err != nil {
		roll.Close()
		return err
	}
	if err := roll.openFor(now); err != nil {
		roll.Close()
		return err
	}
	runner, lastPrice, err := s.resumeRunner(candidate.Policy, roll)
	if err != nil {
		roll.Close()
		return err
	}
	if err := s.roll.Close(); err != nil {
		roll.Close()
		return err
	}
	s.policy, s.policySHA256 = candidate.Policy, candidate.CandidatePolicySHA256
	s.roll, s.runner, s.lastPrice = roll, runner, lastPrice
	s.consecutiveUnavailable, s.dataUnavailable = 0, false
	if len(roll.Records()) != 0 {
		ticks, err := shadowTicksFrom(roll.Records(), s.policy, true)
		if err != nil {
			return err
		}
		if err := s.reconcileAlertTicks(ticks); err != nil {
			return err
		}
	}
	if err := s.reconcileStoredShadowReports(); err != nil {
		return err
	}
	return s.alertStrategy(now, paperstatus.KindStrategyChanged)
}

func (s *shadowRun) rollDay(now time.Time, output io.Writer) (bool, error) {
	if !s.roll.RolledOver(now) {
		return false, nil
	}
	from, err := time.Parse("2006-01-02", s.roll.Day())
	if err != nil {
		return false, err
	}
	periodEnd := from.Add(24 * time.Hour)
	if err := s.runner.ClosePeriod(periodEnd.Add(-time.Nanosecond), s.lastPrice); err != nil {
		return false, err
	}
	if err := s.finishDayAt(output, periodEnd); err != nil {
		return false, err
	}
	if s.policy.MarketEvidenceClass == shadow.MarketEvidenceDevelopmentProvisional {
		return false, errProvisionalPaperComplete
	}
	if s.policy.Version == shadow.AdmittedVersion &&
		!s.admissionThrough.Equal(now.UTC().Truncate(24*time.Hour)) {
		return false, errors.New("market admission evidence must be refreshed before the next paper day")
	}
	previousPolicy := s.policySHA256
	if err := s.refreshSelectedCandidate(now); err != nil {
		return false, err
	}
	strategyChanged := s.policySHA256 != previousPolicy
	// A new day resets the operational canary with its own opening mark.
	if s.roll.Day() != dayKey(now) {
		if err := s.roll.advanceTo(now); err != nil {
			return false, err
		}
		fresh, err := s.newRunner()
		if err != nil {
			return false, err
		}
		s.runner, s.lastPrice = fresh, 0
	}
	s.activationSequence = 0
	if !strategyChanged {
		if err := s.alertStrategy(now, paperstatus.KindStrategyActive); err != nil {
			return false, err
		}
	}
	return true, nil
}

// drive is the only place that knows about wall-clock time. It reads every
// source first, then establishes one event time for validation, decisions, and
// the journal. A UTC rollover discards the old runner's prepared observation
// before it can mutate or write the new day's evidence.
func (s *shadowRun) drive(ctx context.Context, once bool, output io.Writer) error {
	ticker := time.NewTicker(s.policy.Tick())
	defer ticker.Stop()

	for {
		now := time.Now().UTC()
		if _, err := s.rollDay(now, output); err != nil {
			if errors.Is(err, errProvisionalPaperComplete) {
				return nil
			}
			return err
		}
		observation := s.runner.Observe(ctx)
		now = time.Now().UTC()
		rolled, err := s.rollDay(now, output)
		if err != nil {
			if errors.Is(err, errProvisionalPaperComplete) {
				return nil
			}
			return err
		}
		if rolled {
			continue
		}
		observation = s.runner.ApplyNativePriceCeiling(now, observation, s.portfolioMaxSOL)
		nextSell := s.runner.NextSell()
		tick, err := s.runner.StepObservation(ctx, now, observation)
		if err != nil {
			return err
		}
		if tick.PriceMicros != 0 {
			s.lastPrice = tick.PriceMicros
		}
		if err := printShadowTick(output, tick); err != nil {
			return err
		}
		if err := s.alertTick(tick, nextSell); err != nil {
			return err
		}
		if err := s.updatePaperCurrent(tick, s.runner.NextSell()); err != nil {
			return err
		}
		if once {
			return nil
		}
		select {
		case <-ctx.Done():
			// A clean stop still produces the day's report, so an interrupted
			// run is not a silently discarded one.
			if err := s.runner.ClosePeriod(tick.At, s.lastPrice); err != nil {
				return err
			}
			return s.finishDayAt(output, tick.At)
		case <-ticker.C:
		}
	}
}

func printShadowTick(output io.Writer, tick shadow.Tick) error {
	encoder := json.NewEncoder(output)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(tick)
}

// finishDay writes the period report next to the journal it came from.
func (s *shadowRun) finishDay(output io.Writer) error {
	if s.roll.Day() == "" {
		return nil
	}
	from, err := time.Parse("2006-01-02", s.roll.Day())
	if err != nil {
		return err
	}
	periodEnd := from.Add(24 * time.Hour)
	if err := s.runner.ClosePeriod(periodEnd.Add(-time.Nanosecond), s.lastPrice); err != nil {
		return err
	}
	return s.finishDayAt(output, periodEnd)
}

func (s *shadowRun) finishDayAt(output io.Writer, periodEnd time.Time) error {
	day := s.roll.Day()
	if day == "" {
		return nil
	}
	from, err := time.Parse("2006-01-02", day)
	if err != nil {
		return err
	}
	if !periodEnd.After(from) || periodEnd.After(from.Add(24*time.Hour)) {
		return errors.New("shadow report end is outside its UTC day")
	}
	if s.lastPrice == 0 {
		// No price means there is no honest P&L report, but silence would leave
		// Telegram claiming the strategy is still active after a clean stop.
		return s.alertUnavailableReport(from, periodEnd)
	}
	// Derived by replaying the day's whole journal, not from this process's
	// counters. A runner that restarted mid-day would otherwise report only
	// its own share of the day, and the stored report would not match the
	// record it sits beside.
	ticks, err := shadowTicksFrom(s.roll.Records(), s.policy, false)
	if err != nil {
		return err
	}
	report, err := buildShadowReport(s.policy, day, ticks)
	if err != nil || !report.To.Equal(periodEnd) {
		return errors.New("shadow journal does not contain the report period end")
	}
	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	path := filepath.Join(s.roll.directory, "report-"+day+".json")
	if err := securefile.ReplacePrivate(path, append(encoded, '\n'), maxInputBytes); err != nil {
		return errors.New("could not write the shadow report")
	}
	if err := s.alertReport(report); err != nil {
		return err
	}
	return report.Render(output)
}

func periodCloseCount(ticks []shadow.Tick) uint64 {
	var count uint64
	for _, tick := range ticks {
		if tick.PeriodClose {
			count++
		}
	}
	return count
}

// reconcileMissingShadowReports closes the journal-to-report crash window. A
// period-close record is the durable fact; this rebuilds only a missing
// derived report and never changes the journal.
func (s *shadowRun) reconcileMissingShadowReports() error {
	paths, err := filepath.Glob(filepath.Join(s.roll.directory, "shadow-*.jsonl"))
	if err != nil {
		return errors.New("could not list shadow journals")
	}
	if len(paths) > paperstatus.MaxEvents {
		paths = paths[len(paths)-paperstatus.MaxEvents:]
	}
	for _, path := range paths {
		name := filepath.Base(path)
		day := name[len("shadow-") : len(name)-len(".jsonl")]
		from, err := time.Parse("2006-01-02", day)
		if err != nil {
			return errors.New("shadow journal filename has an invalid UTC day")
		}
		reportPath := filepath.Join(s.roll.directory, "report-"+day+".json")
		if _, err := os.Lstat(reportPath); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return errors.New("could not inspect the shadow report")
		}
		ticks, err := readShadowTicks(path, s.policy)
		if err != nil {
			return err
		}
		if len(ticks) == 0 || !ticks[len(ticks)-1].PeriodClose {
			continue
		}
		if lastShadowPrice(ticks) == 0 {
			periodEnd, err := shadowReportEnd(from, ticks[len(ticks)-1].At)
			if err != nil {
				return err
			}
			previous := s.reconcilingAlerts
			s.reconcilingAlerts = true
			alertErr := s.alertUnavailableReport(from, periodEnd)
			s.reconcilingAlerts = previous
			if alertErr != nil {
				return alertErr
			}
			continue
		}
		report, err := buildShadowReport(s.policy, day, ticks)
		if err != nil {
			return err
		}
		encoded, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		if err := securefile.CreatePrivate(reportPath, append(encoded, '\n'), maxInputBytes); err != nil {
			return errors.New("could not recover the shadow report")
		}
		previous := s.reconcilingAlerts
		s.reconcilingAlerts = true
		alertErr := s.alertReport(report)
		s.reconcilingAlerts = previous
		if alertErr != nil {
			return alertErr
		}
	}
	return nil
}

func buildShadowReport(policy shadow.Policy, day string, ticks []shadow.Tick) (shadow.Report, error) {
	from, err := time.Parse("2006-01-02", day)
	if err != nil {
		return shadow.Report{}, err
	}
	replayed, err := shadow.Replay(policy, ticks)
	if err != nil {
		return shadow.Report{}, err
	}
	periodEnd, err := shadowReportEnd(from, replayed.PeriodEnd)
	if err != nil {
		return shadow.Report{}, err
	}
	return shadow.BuildReport(
		policy, replayed.Ledger, replayed.Counts, replayed.Stats,
		replayed.ClosingPrice, from, periodEnd,
	)
}

// reconcileStoredShadowReports recovers the second journal-to-alert crash
// window, including a report written just before a UTC rollover. Only the
// retained alert capacity is scanned; older periods are already represented by
// the bounded projection's dropped-event warning.
func (s *shadowRun) reconcileStoredShadowReports() error {
	if s.alerts == nil {
		return nil
	}
	paths, err := filepath.Glob(filepath.Join(s.roll.directory, "report-*.json"))
	if err != nil {
		return errors.New("could not list stored shadow reports")
	}
	if len(paths) > paperstatus.MaxEvents {
		paths = paths[len(paths)-paperstatus.MaxEvents:]
	}
	for _, path := range paths {
		name := filepath.Base(path)
		day := name[len("report-") : len(name)-len(".json")]
		var ticks []shadow.Tick
		if day == s.roll.Day() {
			ticks, err = shadowTicksFrom(s.roll.Records(), s.policy, false)
		} else {
			ticks, err = readShadowTicks(filepath.Join(s.roll.directory, "shadow-"+day+".jsonl"), s.policy)
		}
		if err != nil {
			return err
		}
		if err := s.reconcileStoredShadowReport(path, day, ticks); err != nil {
			return err
		}
	}
	return nil
}

// reconcileStoredShadowReport derives one stored report from the hash-chained
// journal prefix that existed at its recorded end. A later restart may have
// appended more observations to the same day's journal.
func (s *shadowRun) reconcileStoredShadowReport(path, day string, ticks []shadow.Tick) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return errors.New("could not inspect the stored shadow report")
	}
	var stored shadow.Report
	if err := readStrictJSON(path, &stored); err != nil {
		return errors.New("the stored shadow report is invalid")
	}
	prefix := make([]shadow.Tick, 0, len(ticks))
	for _, tick := range ticks {
		if !tick.At.After(stored.To) {
			prefix = append(prefix, tick)
		}
	}
	recomputed, err := buildShadowReport(s.policy, day, prefix)
	if err != nil {
		return errors.New("the stored shadow report has no valid journaled period end")
	}
	if len(shadow.Compare(stored, recomputed)) != 0 {
		return errors.New("the stored shadow report disagrees with the journal")
	}
	previous := s.reconcilingAlerts
	s.reconcilingAlerts = true
	defer func() { s.reconcilingAlerts = previous }()
	return s.alertReport(recomputed)
}

// shadowReportEnd converts the last nanosecond of a UTC day back to the
// exclusive midnight boundary shown in reports. The close marker has to stay
// in the day's own journal, while the report period ends at the next midnight.
func shadowReportEnd(from, recorded time.Time) (time.Time, error) {
	dayEnd := from.Add(24 * time.Hour)
	if recorded.Equal(dayEnd.Add(-time.Nanosecond)) {
		return dayEnd, nil
	}
	if !recorded.After(from) || recorded.After(dayEnd) {
		return time.Time{}, errors.New("shadow journal period end is outside its UTC day")
	}
	return recorded, nil
}

// shadowQuoter adapts the read-only Orca quote client to the narrow interface
// shadow mode needs. It can return amounts and nothing else: the instructions
// the client also returns are deliberately discarded here, so there is no path
// from a shadow quote to a transaction.
type shadowQuoter struct {
	client      *swapbuilder.Client
	pool        string
	inputMint   string
	outputMint  string
	primarySell bool
}

func newShadowQuoter(policy shadow.Policy, options shadowRunOptions) (shadow.Quoter, error) {
	route := policy.QuoteRoute
	if err := validateShadowRouteOverrides(route, options); err != nil {
		return nil, err
	}
	if route.Provider == shadow.QuoteJupiter {
		if options.nodeCommand != "" || options.quoteScript != "" || options.pool != "" {
			return nil, errors.New("Jupiter shadow quotes do not use --node-command, --quote-script, or --pool")
		}
		client, err := jupiterquote.New(os.Getenv(jupiterAPIKeyEnvironment))
		if err != nil {
			return nil, err
		}
		return &jupiterShadowQuoter{
			client: client, inputMint: route.InputMint, outputMint: route.OutputMint,
			primarySell: policy.IsSell(),
		}, nil
	}
	if route.Provider != shadow.QuoteOrca {
		return nil, errors.New("shadow quote provider must be orca or jupiter")
	}
	if options.nodeCommand == "" || options.quoteScript == "" {
		return nil, errors.New(
			"Orca shadow run requires --node-command and --quote-script")
	}
	// The quote adapter needs its own endpoint: it requires TLS, while the
	// price reader may be pointed at a loopback node. When only one is set,
	// the shadow endpoint is used and swapbuilder enforces its own TLS rule.
	quoteURL := os.Getenv(quoteEndpointEnvironment)
	if quoteURL == "" {
		quoteURL = os.Getenv(shadowEndpointEnvironment)
	}
	client, err := swapbuilder.New(swapbuilder.Config{
		NodeCommand: options.nodeCommand, ScriptPath: options.quoteScript,
		RPCURL: quoteURL,
	})
	if err != nil {
		return nil, err
	}
	return &shadowQuoter{
		client: client, pool: route.Pool,
		inputMint: route.InputMint, outputMint: route.OutputMint,
		primarySell: policy.IsSell(),
	}, nil
}

func validateShadowRouteOverrides(route shadow.QuoteRoute, options shadowRunOptions) error {
	for name, values := range map[string][2]string{
		"quote provider": {options.quoteSource, route.Provider},
		"pool":           {options.pool, route.Pool},
		"input mint":     {options.inputMint, route.InputMint},
		"output mint":    {options.outputMint, route.OutputMint},
	} {
		if values[0] != "" && values[0] != values[1] {
			return errors.New("shadow " + name + " does not match the policy")
		}
	}
	return nil
}

func (q *shadowQuoter) Quote(
	ctx context.Context,
	owner string,
	sell bool,
	inputAmount uint64,
	slippageBPS uint16,
) (shadow.Quote, error) {
	inputMint := q.inputMint
	if sell != q.primarySell {
		inputMint = q.outputMint
	}
	if inputMint == "" {
		return shadow.Quote{}, errors.New("shadow quote direction has no input mint")
	}
	result, err := q.client.Quote(ctx, swapbuilder.Request{
		Owner: owner, Pool: q.pool, InputMint: inputMint,
		InputAmount: inputAmount, SlippageBPS: slippageBPS,
	})
	if err != nil {
		return shadow.Quote{}, err
	}
	return shadow.Quote{
		InputAmount:     result.TokenIn,
		EstimatedOutput: result.TokenEstOut,
		MinimumOutput:   result.TokenMinOut,
		ReceivedAt:      time.Now().UTC(),
	}, nil
}

// jupiterShadowQuoter exposes only the three amounts shadow accounting needs.
// Jupiter's executable instructions never cross this interface.
type jupiterShadowQuoter struct {
	client      *jupiterquote.Client
	inputMint   string
	outputMint  string
	primarySell bool
}

func (q *jupiterShadowQuoter) Quote(
	ctx context.Context,
	owner string,
	sell bool,
	inputAmount uint64,
	slippageBPS uint16,
) (shadow.Quote, error) {
	inputMint, outputMint := q.inputMint, q.outputMint
	if sell != q.primarySell {
		inputMint, outputMint = outputMint, inputMint
	}
	result, err := q.client.Quote(ctx, jupiterquote.Request{
		Taker: owner, InputMint: inputMint, OutputMint: outputMint,
		InputAmount: inputAmount, SlippageBPS: slippageBPS,
	})
	if err != nil {
		return shadow.Quote{}, err
	}
	return shadow.Quote{
		InputAmount:     result.InputAmount,
		EstimatedOutput: result.EstimatedOutput,
		MinimumOutput:   result.MinimumOutput,
		ReceivedAt:      time.Now().UTC(),
	}, nil
}
