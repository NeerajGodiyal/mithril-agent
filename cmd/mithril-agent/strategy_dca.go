package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"time"
)

const maxDCADays = uint64(365)

type dcaPlan struct {
	Status                       string   `json:"status"`
	Direction                    string   `json:"direction"`
	Days                         uint64   `json:"days"`
	TradesPerDay                 uint64   `json:"trades_per_day"`
	Actions                      uint64   `json:"actions"`
	ScheduleWindowSeconds        uint64   `json:"schedule_window_seconds"`
	BudgetLamports               uint64   `json:"budget_lamports"`
	BudgetSOL                    string   `json:"budget_sol"`
	BudgetUSD                    string   `json:"budget_usd,omitempty"`
	ReferenceSOLUSD              string   `json:"reference_sol_usd,omitempty"`
	BudgetScope                  string   `json:"budget_scope"`
	FeesAndRentAdditional        bool     `json:"fees_and_rent_additional"`
	ExecutionModel               string   `json:"execution_model"`
	MainnetExecution             string   `json:"mainnet_execution"`
	InputLamportsPerAction       uint64   `json:"input_lamports_per_action"`
	InputSOLPerAction            string   `json:"input_sol_per_action"`
	PlannedInputLamports         uint64   `json:"planned_input_lamports"`
	UnallocatedRemainderLamports uint64   `json:"unallocated_remainder_lamports"`
	SetupFlags                   []string `json:"setup_flags"`
	DailyEnableTemplate          []string `json:"daily_enable_template"`
	RequiresDailyAuthorization   bool     `json:"requires_daily_authorization"`
	Authorized                   bool     `json:"authorized"`
}

// strategyDCAPlan deliberately stops at arithmetic. The existing setup and
// enable commands own route discovery, independent evidence, signing policy,
// and the expiring grant; duplicating any of them here would create a second,
// weaker authorization path.
func strategyDCAPlan(args []string, output io.Writer) error {
	flags := flag.NewFlagSet("strategy dca-plan", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	totalSOL := flags.String("total-sol", "", "exact maximum SOL input")
	budgetUSD := flags.String("budget-usd", "", "USD notional converted once at the named reference price")
	referenceSOLUSD := flags.String("reference-sol-usd", "", "operator-confirmed SOL/USD reference price")
	days := flags.Uint64("days", 0, "calendar days, 1..365")
	tradesPerDay := flags.Uint64("trades-per-day", 1, "fixed actions in each UTC day, 1..100")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, strategyUsage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *days == 0 || *days > maxDCADays {
		return fmt.Errorf("strategy dca-plan requires --days from 1 to %d and no positional arguments", maxDCADays)
	}
	if *tradesPerDay == 0 || *tradesPerDay > maxTradesPerDay || 86_400%*tradesPerDay != 0 {
		return fmt.Errorf("--trades-per-day must be from 1 to %d and divide one UTC day evenly", maxTradesPerDay)
	}
	budgetLamports, err := dcaBudgetLamports(*totalSOL, *budgetUSD, *referenceSOLUSD)
	if err != nil {
		return err
	}
	actions := *days * *tradesPerDay
	perAction := budgetLamports / actions
	if perAction == 0 {
		return errors.New("DCA budget is too small to allocate at least one lamport to every action")
	}
	planned := perAction * actions
	window := uint64((24 * time.Hour) / time.Duration(*tradesPerDay) / time.Second)
	plan := dcaPlan{
		Status: "planned_not_authorized", Direction: "sell_sol_for_usdc",
		Days: *days, TradesPerDay: *tradesPerDay, Actions: actions,
		ScheduleWindowSeconds: window,
		BudgetLamports:        budgetLamports, BudgetSOL: formatUnits(budgetLamports, 9),
		BudgetUSD: *budgetUSD, ReferenceSOLUSD: *referenceSOLUSD,
		BudgetScope: "swap_input_only", FeesAndRentAdditional: true,
		ExecutionModel: "self_managed_bounded_runner", MainnetExecution: "disabled",
		InputLamportsPerAction: perAction, InputSOLPerAction: formatUnits(perAction, 9),
		PlannedInputLamports:         planned,
		UnallocatedRemainderLamports: budgetLamports - planned,
		SetupFlags: []string{
			"--direction", "sell",
			"--input-lamports", fmt.Sprint(perAction),
			"--trades-per-day", fmt.Sprint(*tradesPerDay),
			"--schedule-window", (time.Duration(window) * time.Second).String(),
		},
		DailyEnableTemplate: []string{
			"strategy", "enable", "--duration", "24h",
			"--max-trades", fmt.Sprint(*tradesPerDay),
			"--allow-any-price", "--reason", "DCA day YYYY-MM-DD",
		},
		RequiresDailyAuthorization: *days > 1,
		Authorized:                 false,
	}
	return json.NewEncoder(output).Encode(plan)
}

func dcaBudgetLamports(totalSOL, budgetUSD, referenceSOLUSD string) (uint64, error) {
	if (totalSOL == "") == (budgetUSD == "") {
		return 0, errors.New("give exactly one of --total-sol or --budget-usd")
	}
	if totalSOL != "" {
		if referenceSOLUSD != "" {
			return 0, errors.New("--reference-sol-usd is only valid with --budget-usd")
		}
		return parseDecimalUnits9(totalSOL, "DCA SOL budget")
	}
	if referenceSOLUSD == "" {
		return 0, errors.New("--budget-usd requires --reference-sol-usd; the planner never invents a market price")
	}
	budgetMicros, err := parseDecimalUnits(budgetUSD, "DCA USD budget", ^uint64(0))
	if err != nil {
		return 0, err
	}
	priceMicros, err := parseUSDThreshold(referenceSOLUSD, "reference SOL/USD price")
	if err != nil {
		return 0, err
	}
	lamports := new(big.Int).Mul(new(big.Int).SetUint64(budgetMicros), big.NewInt(1_000_000_000))
	lamports.Quo(lamports, new(big.Int).SetUint64(priceMicros))
	if !lamports.IsUint64() || lamports.Sign() == 0 {
		return 0, errors.New("DCA USD budget is outside the supported SOL range")
	}
	return lamports.Uint64(), nil
}
