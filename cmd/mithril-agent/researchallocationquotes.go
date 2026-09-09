package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/Overclock-Validator/mithril-agent/journal"
	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
)

const researchAllocationQuotesUsage = `Usage: mithril-agent research allocation-quotes --generation DIR --market sol|jup --role pre-champion|champion [--max-age 30s] [--include-inventory]

Reads two sequential Jupiter Metis quotes for the actual allocation policy's
initial lot, then its hypothetical reverse. This is not the current position
or the next dynamically sized order. With --include-inventory, also quotes selling
the full base-token inventory in a verified paper prefix no older than two minutes.
That prefix must remain unchanged during collection. This is a liquidation-size
diagnostic, not an order recommendation; separately held fee reserves are excluded.
Requires MITHRIL_AGENT_JUPITER_API_KEY.
Amounts are raw token units; priceImpactPct is a decimal ratio, not a percentage.
Freshness bounds local response receipts, not provider creation or price validity.
Route loss is not an all-in cost: network/priority fees, account rent, failed
transactions and movement before execution are not estimated. Sequential quotes
do not guarantee a round trip or profit. This diagnostic cannot qualify a market,
change policy, sign or submit; the host must separately verify service health.`

type researchAllocationQuotes struct {
	Version               uint32                    `json:"version"`
	Status                string                    `json:"status"`
	Market                string                    `json:"market"`
	PolicySHA256          string                    `json:"policy_sha256"`
	Binding               researchAllocationBinding `json:"binding"`
	RoleBindingVerified   bool                      `json:"role_binding_verified"`
	ProcessHealthVerified bool                      `json:"process_health_verified"`
	RecordedBasisEligible bool                      `json:"recorded_basis_eligible"`
	SizeBasis             string                    `json:"size_basis"`
	QuoteSource           string                    `json:"quote_source"`
	Verification          string                    `json:"verification"`
	PriceImpactUnit       string                    `json:"price_impact_unit"`
	ReceivedAtBasis       string                    `json:"received_at_basis"`
	AllInCostsKnown       bool                      `json:"all_in_costs_known"`
	RoundTripGuaranteed   bool                      `json:"round_trip_guaranteed"`
	InputDecimals         uint8                     `json:"input_decimals"`
	OutputDecimals        uint8                     `json:"output_decimals"`
	SlippageBPS           uint16                    `json:"slippage_bps"`
	CheckedAt             time.Time                 `json:"checked_at"`
	MaxReceiptAgeMillis   int64                     `json:"max_receipt_age_millis"`
	Initial               shadowMarketCurveQuote    `json:"initial"`
	Reverse               shadowMarketCurveQuote    `json:"reverse"`
	RoundTripRouteLossBPS uint16                    `json:"round_trip_route_loss_bps"`
	Inventory             *researchInventoryQuote   `json:"inventory,omitempty"`
}

type researchInventoryQuote struct {
	Status          string                  `json:"status"`
	SizeBasis       string                  `json:"size_basis"`
	BaseUnits       uint64                  `json:"base_units,string"`
	InputDecimals   uint8                   `json:"input_decimals"`
	OutputDecimals  uint8                   `json:"output_decimals"`
	ObservedThrough time.Time               `json:"observed_through"`
	MarkPublishedAt time.Time               `json:"mark_published_at"`
	Journal         journal.DurablePrefix   `json:"journal"`
	Quote           *shadowMarketCurveQuote `json:"quote,omitempty"`
}

func runResearchAllocationQuotes(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("research allocation-quotes", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	generation := flags.String("generation", "", "exact protected allocation generation")
	market := flags.String("market", "", "sol or jup")
	role := flags.String("role", "", "pre-champion or champion")
	maxAge := flags.Duration("max-age", 30*time.Second, "maximum local response receipt age")
	includeInventory := flags.Bool("include-inventory", false, "also quote the verified current base inventory")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, err = fmt.Fprintln(output, researchAllocationQuotesUsage)
		}
		return err
	}
	if flags.NArg() != 0 || !cleanResearchPath(*generation) ||
		(*market != "sol" && *market != "jup") || (*role != "pre-champion" && *role != "champion") ||
		*maxAge < time.Millisecond || *maxAge > time.Minute {
		return errors.New("allocation quotes requires a generation, supported market role and receipt age between one millisecond and one minute")
	}
	key := os.Getenv(jupiterAPIKeyEnvironment)
	if strings.TrimSpace(key) == "" {
		return errors.New("allocation quotes requires the configured Jupiter API key")
	}
	quotes, err := jupiterquote.New(key)
	if err != nil {
		return errors.New("allocation quote client is unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	artifact, err := collectResearchAllocationQuotes(ctx, *generation, *market, *role, *maxAge, quotes, time.Now, *includeInventory)
	if err != nil {
		return err
	}
	return json.NewEncoder(output).Encode(artifact)
}

func collectResearchAllocationQuotes(
	ctx context.Context,
	generation, market, role string,
	maxAge time.Duration,
	quotes shadowMarketCurveQuoteSource,
	now func() time.Time,
	includeInventory bool,
) (researchAllocationQuotes, error) {
	if ctx == nil || quotes == nil || now == nil || maxAge < time.Millisecond || maxAge > time.Minute {
		return researchAllocationQuotes{}, errors.New("allocation quote source or receipt age is invalid")
	}
	if err := ctx.Err(); err != nil {
		return researchAllocationQuotes{}, err
	}
	started := now().UTC()
	source, err := resolveResearchAllocation(generation, market, role, started)
	if err != nil {
		return researchAllocationQuotes{}, err
	}
	policySHA, err := source.policy.Fingerprint()
	if err != nil {
		return researchAllocationQuotes{}, err
	}
	artifact := researchAllocationQuotes{
		Version: 1, Status: "diagnostic_only", Market: shadowMarketPair(source.policy),
		PolicySHA256: policySHA, Binding: source.binding, RoleBindingVerified: true,
		SizeBasis: "initial_policy_lot", QuoteSource: "jupiter_swap_v2_build_metis",
		Verification: "single_provider", PriceImpactUnit: "decimal_ratio", ReceivedAtBasis: "host_response_receipt",
		InputDecimals: source.policy.InputDecimals, OutputDecimals: source.policy.OutputDecimals,
		SlippageBPS: source.policy.SlippageBPS, MaxReceiptAgeMillis: maxAge.Milliseconds(),
	}
	request := jupiterquote.Request{
		Taker: source.policy.Observe, InputMint: source.policy.QuoteRoute.InputMint,
		OutputMint: source.policy.QuoteRoute.OutputMint, InputAmount: source.policy.InputAmount,
		SlippageBPS: source.policy.SlippageBPS,
	}
	legs := []*shadowMarketCurveQuote{&artifact.Initial, &artifact.Reverse}
	if includeInventory {
		performance, err := buildResearchPerformance(source.policy, source.directory, started, 2*time.Minute)
		if err != nil {
			return researchAllocationQuotes{}, err
		}
		artifact.Inventory = &researchInventoryQuote{
			Status: "no_base_inventory", SizeBasis: "journal_base_inventory",
			BaseUnits: performance.BaseUnits, InputDecimals: performance.BaseDecimals, OutputDecimals: performance.QuoteDecimals,
			ObservedThrough: performance.ObservedThrough, MarkPublishedAt: performance.MarkPublishedAt, Journal: performance.Journal,
		}
		if performance.BaseUnits > 0 {
			artifact.Inventory.Status = "quoted"
			artifact.Inventory.Quote = &shadowMarketCurveQuote{}
			legs = append(legs, artifact.Inventory.Quote)
		}
	}
	previous := started
	for index, leg := range legs {
		if index == 2 {
			request.InputMint, request.OutputMint = source.policy.QuoteRoute.InputMint, source.policy.QuoteRoute.OutputMint
			if !source.policy.IsSell() {
				request.InputMint, request.OutputMint = request.OutputMint, request.InputMint
			}
			request.InputAmount = artifact.Inventory.BaseUnits
		}
		if err := ctx.Err(); err != nil {
			return researchAllocationQuotes{}, err
		}
		from := now().UTC()
		if from.Before(previous) || dayKey(from) != dayKey(started) {
			return researchAllocationQuotes{}, errors.New("allocation quote clock changed during collection")
		}
		result, err := quotes.Quote(ctx, request)
		through := now().UTC()
		if err != nil {
			return researchAllocationQuotes{}, errors.New("allocation route quote is unavailable")
		}
		*leg = newShadowMarketCurveQuote(request, result, shadowMarketCurveElapsedMillis(from, through))
		if through.Before(from) || result.ReceivedAt.Before(from) || through.Sub(from) > maxAge ||
			shadowMarketCurveQuoteValid(*leg, request, through) != nil {
			return researchAllocationQuotes{}, errors.New("allocation route quote values or receipt interval are invalid")
		}
		previous = through
		request.InputMint, request.OutputMint = request.OutputMint, request.InputMint
		request.InputAmount = result.EstimatedOutput
	}
	after, err := resolveResearchAllocation(generation, market, role, previous)
	if err != nil {
		return researchAllocationQuotes{}, err
	}
	if includeInventory {
		performance, err := buildResearchPerformance(after.policy, after.directory, now().UTC(), 2*time.Minute)
		if err != nil || performance.Journal != artifact.Inventory.Journal || performance.BaseUnits != artifact.Inventory.BaseUnits {
			return researchAllocationQuotes{}, errors.New("allocation inventory prefix changed or became unavailable during collection")
		}
	}
	finished := now().UTC()
	if ctx.Err() != nil || finished.Before(previous) || dayKey(started) != dayKey(finished) ||
		!sameResearchAllocation(source, after) || finished.Sub(artifact.Initial.ReceivedAt) > maxAge {
		return researchAllocationQuotes{}, errors.New("allocation quote identity or receipt freshness changed during collection")
	}
	if includeInventory && (finished.Sub(artifact.Inventory.ObservedThrough) > 2*time.Minute ||
		finished.Sub(artifact.Inventory.MarkPublishedAt) > 2*time.Minute) {
		return researchAllocationQuotes{}, errors.New("allocation inventory or valuation became stale during collection")
	}
	artifact.CheckedAt = finished
	if artifact.Reverse.EstimatedOutput < artifact.Initial.InputAmount {
		loss, ok := boundedMulDivCeil(artifact.Initial.InputAmount-artifact.Reverse.EstimatedOutput, 10_000, artifact.Initial.InputAmount)
		if !ok {
			return researchAllocationQuotes{}, errors.New("allocation route loss cannot be represented")
		}
		artifact.RoundTripRouteLossBPS = uint16(loss)
	}
	return artifact, nil
}
