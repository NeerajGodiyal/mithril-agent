package swaprun

import (
	"context"
	"errors"
	"math/bits"

	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/pricetrigger"
	"github.com/Overclock-Validator/mithril-agent/swapbuilder"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

type routeIntent struct {
	RecentBlockhash   string
	InputAmount       uint64
	MinimumOutput     uint64
	RentLamports      uint64
	OutputAccountMade bool
	TemporaryAccount  string
}

type buyTransactor interface {
	VerifyWhirlpoolBuyDeployment(context.Context, orcaswap.BuyPolicyV2, uint64) error
	VerifyTokenInputAccount(
		context.Context,
		string,
		string,
		string,
		uint64,
		uint64,
	) (txflow.TokenAccountEvidence, error)
	ReconcileBuyExpected(
		context.Context,
		txflow.Submission,
		txflow.ExpectedBuy,
		uint64,
	) (txflow.Reconciliation, error)
}

func (p Profile) isBuy() bool {
	return p.Name == orcaswap.BuyProfileName && p.Version == orcaswap.BuyProfileVersion
}

func (p Profile) IsBuy() bool { return p.isBuy() }

func (p Profile) owner() string {
	if p.isBuy() && p.BuyRoute != nil {
		return p.BuyRoute.Owner
	}
	return p.Route.Owner
}

func (p Profile) Owner() string { return p.owner() }

func (p Profile) pool() string {
	if p.isBuy() && p.BuyRoute != nil {
		return p.BuyRoute.Pool
	}
	return p.Route.Pool
}

func (p Profile) Pool() string { return p.pool() }

func (p Profile) inputMint() string {
	if p.isBuy() && p.BuyRoute != nil {
		return p.BuyRoute.TokenMintB
	}
	return p.Route.InputMint
}

func (p Profile) InputMint() string { return p.inputMint() }

func (p Profile) inputAmount() uint64 {
	if p.isBuy() {
		return p.InputTokenAmount
	}
	return p.InputLamports
}

func (p Profile) InputAmount() uint64 { return p.inputAmount() }

func (p Profile) maxRouteRent() uint64 {
	if p.isBuy() && p.BuyRoute != nil {
		return p.BuyRoute.MaxTemporaryRentLamports
	}
	return p.Route.MaxOutputAccountRentLamports
}

func (p Profile) requestDomain() string {
	if p.isBuy() {
		return orcaswap.BuyRequestDomain
	}
	return orcaswap.RequestDomain
}

func routeActionID(profile Profile, fingerprint string, windowStart int64) (string, error) {
	if profile.isBuy() {
		return orcaswap.ComputeBuyActionID(fingerprint, windowStart)
	}
	return orcaswap.ComputeActionID(fingerprint, windowStart)
}

func validateRouteQuote(profile Profile, quote swapbuilder.Result) (routeIntent, error) {
	approved := orcaswap.Quote{
		InputAmount: quote.TokenIn, EstimatedOutput: quote.TokenEstOut,
		MinimumOutput: quote.TokenMinOut, SlippageBPS: profile.SlippageBPS,
	}
	if profile.isBuy() {
		if profile.BuyRoute == nil {
			return routeIntent{}, errors.New("buy route is missing")
		}
		intent, err := orcaswap.ValidateBuyInstructionsV2(*profile.BuyRoute, approved, quote.Instructions)
		if err != nil {
			return routeIntent{}, err
		}
		return routeIntent{
			InputAmount: intent.InputAmount, MinimumOutput: intent.MinimumOutputLamports,
			RentLamports:     intent.TemporaryRentLamports,
			TemporaryAccount: intent.TemporaryWSOLAccount,
		}, nil
	}
	intent, err := orcaswap.ValidateInstructions(profile.Route, approved, quote.Instructions)
	if err != nil {
		return routeIntent{}, err
	}
	return routeIntent{
		InputAmount: intent.InputAmount, MinimumOutput: intent.MinimumOutput,
		OutputAccountMade: intent.OutputAccountCreated,
	}, nil
}

func decodeRouteMessage(profile Profile, message []byte) (routeIntent, error) {
	if profile.isBuy() {
		if profile.BuyRoute == nil {
			return routeIntent{}, errors.New("buy route is missing")
		}
		intent, err := orcaswap.DecodeBuyMessageV2(*profile.BuyRoute, message)
		if err != nil {
			return routeIntent{}, err
		}
		return routeIntent{
			RecentBlockhash: intent.RecentBlockhash,
			InputAmount:     intent.InputAmount, MinimumOutput: intent.MinimumOutputLamports,
			RentLamports:     intent.TemporaryRentLamports,
			TemporaryAccount: intent.TemporaryWSOLAccount,
		}, nil
	}
	intent, err := orcaswap.DecodeMessage(profile.Route, message)
	if err != nil {
		return routeIntent{}, err
	}
	return routeIntent{
		RecentBlockhash: intent.RecentBlockhash,
		InputAmount:     intent.InputAmount, MinimumOutput: intent.MinimumOutput,
		OutputAccountMade: intent.OutputAccountCreated,
	}, nil
}

func verifyRouteDeployment(
	ctx context.Context,
	transactor Transactor,
	profile Profile,
	minContextSlot uint64,
) error {
	if profile.isBuy() {
		buy, ok := transactor.(buyTransactor)
		if !ok || profile.BuyRoute == nil {
			return errors.New("buy transaction verification is unavailable")
		}
		return buy.VerifyWhirlpoolBuyDeployment(ctx, *profile.BuyRoute, minContextSlot)
	}
	return transactor.VerifyWhirlpoolDeployment(ctx, profile.Route, minContextSlot)
}

func verifyBuyInput(
	ctx context.Context,
	transactor Transactor,
	profile Profile,
	minContextSlot uint64,
) (txflow.TokenAccountEvidence, error) {
	if !profile.isBuy() {
		return txflow.TokenAccountEvidence{}, nil
	}
	buy, ok := transactor.(buyTransactor)
	if !ok || profile.BuyRoute == nil {
		return txflow.TokenAccountEvidence{}, errors.New("buy token-account verification is unavailable")
	}
	return buy.VerifyTokenInputAccount(
		ctx,
		profile.BuyRoute.InputTokenAccount,
		profile.BuyRoute.TokenMintB,
		profile.BuyRoute.Owner,
		profile.InputTokenAmount,
		minContextSlot,
	)
}

func verifyRouteRent(
	ctx context.Context,
	transactor Transactor,
	profile Profile,
	built *builtRecord,
) error {
	if built == nil {
		return errors.New("swap build evidence is missing")
	}
	if !profile.isBuy() && !built.OutputAccountCreated {
		return nil
	}
	evidence, err := transactor.VerifyTokenAccountRent(ctx, profile.maxRouteRent())
	if err != nil {
		return err
	}
	if profile.isBuy() {
		if built.TemporaryAccountRent == 0 ||
			evidence.Lamports != built.TemporaryAccountRent {
			return errors.New("temporary account rent evidence changed")
		}
		return nil
	}
	if built.OutputAccountRent == 0 || evidence.Lamports != built.OutputAccountRent {
		return errors.New("output account rent evidence changed")
	}
	return nil
}

func reconcileRoute(
	ctx context.Context,
	transactor Transactor,
	profile Profile,
	submission txflow.Submission,
	signature,
	transactionSHA256 string,
	minimumOutput,
	feeLamports uint64,
) (txflow.Reconciliation, error) {
	if profile.isBuy() {
		buy, ok := transactor.(buyTransactor)
		if !ok || profile.BuyRoute == nil {
			return txflow.Reconciliation{}, errors.New("buy reconciliation is unavailable")
		}
		return buy.ReconcileBuyExpected(ctx, submission, txflow.ExpectedBuy{
			Signature: signature, TransactionSHA256: transactionSHA256,
			Policy: *profile.BuyRoute, InputAmount: profile.InputTokenAmount,
			MinimumOutput: minimumOutput,
		}, feeLamports)
	}
	return transactor.ReconcileSwapExpected(ctx, submission, txflow.ExpectedSwap{
		Signature: signature, TransactionSHA256: transactionSHA256,
		Policy: profile.Route, InputAmount: profile.InputLamports,
		MinimumOutput: minimumOutput,
	}, feeLamports)
}

func executablePriceMicros(profile Profile, minimumOutput uint64) (uint64, error) {
	if !profile.isBuy() {
		return priceMicrosForOutput(minimumOutput, profile.InputLamports)
	}
	if minimumOutput == 0 || profile.InputTokenAmount == 0 {
		return 0, errors.New("price amounts are invalid")
	}
	high, low := bits.Mul64(profile.InputTokenAmount, 1_000_000_000)
	if high >= minimumOutput {
		return 0, errors.New("price conversion overflows")
	}
	priceMicros, remainder := bits.Div64(high, low, minimumOutput)
	if remainder != 0 {
		if priceMicros == ^uint64(0) {
			return 0, errors.New("price conversion overflows")
		}
		priceMicros++
	}
	if priceMicros == 0 || priceMicros > pricetrigger.MaxPriceMicros {
		return 0, errors.New("price conversion is outside policy")
	}
	return priceMicros, nil
}
