package txflow

import (
	"bytes"
	"context"
	"errors"

	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

type v0FeeProvider interface {
	FeeForV0Message(context.Context, []byte, map[[32]byte][][32]byte, string, uint64) (solanarpc.FeeQuote, error)
}

type v0SimulationProvider interface {
	SimulateV0(context.Context, []byte, map[[32]byte][][32]byte, uint64) (solanarpc.LegacySimulation, error)
}

// FeeForV0Message requires two independent providers to quote the same fee for
// the exact independently resolved, one-signer message.
func (l *Lifecycle) FeeForV0Message(
	ctx context.Context,
	message []byte,
	addressTables map[[32]byte][][32]byte,
	expectedSigner string,
	minContextSlot uint64,
) (FeeEvidence, error) {
	if err := validateV0Inputs(message, addressTables, expectedSigner, minContextSlot); err != nil {
		return FeeEvidence{}, err
	}
	primary, primaryOK := l.primary.(v0FeeProvider)
	secondary, secondaryOK := l.secondary.(v0FeeProvider)
	if !primaryOK || !secondaryOK {
		return FeeEvidence{}, errors.New("evidence providers do not support v0 fee quotes")
	}
	a, b, err := queryPair(
		ctx,
		func(ctx context.Context) (solanarpc.FeeQuote, error) {
			return primary.FeeForV0Message(
				ctx, bytes.Clone(message), cloneAddressTables(addressTables), expectedSigner, minContextSlot,
			)
		},
		func(ctx context.Context) (solanarpc.FeeQuote, error) {
			return secondary.FeeForV0Message(
				ctx, bytes.Clone(message), cloneAddressTables(addressTables), expectedSigner, minContextSlot,
			)
		},
	)
	if err != nil {
		return FeeEvidence{}, errors.New("query independent v0 message fees")
	}
	if a.ContextSlot < minContextSlot || b.ContextSlot < minContextSlot ||
		a.Lamports == 0 || a.Lamports != b.Lamports {
		return FeeEvidence{}, errors.New("RPC providers disagree on v0 message fee")
	}
	return FeeEvidence{
		Lamports: a.Lamports, MinContextSlot: minContextSlot,
		PrimaryContextSlot: a.ContextSlot, SecondaryContextSlot: b.ContextSlot,
	}, nil
}

// SimulateV0 asks only the operator's Mithril node to execute the unsigned,
// independently resolved message and returns bounded evidence about the run.
func (l *Lifecycle) SimulateV0(
	ctx context.Context,
	message []byte,
	addressTables map[[32]byte][][32]byte,
	expectedSigner string,
	minContextSlot uint64,
) (LegacySimulationEvidence, error) {
	if err := validateV0Inputs(message, addressTables, expectedSigner, minContextSlot); err != nil {
		return LegacySimulationEvidence{}, err
	}
	simulator, ok := l.node.(v0SimulationProvider)
	if !ok {
		return LegacySimulationEvidence{}, errors.New("Mithril RPC does not support v0 simulation")
	}
	simulation, err := simulator.SimulateV0(
		ctx, bytes.Clone(message), cloneAddressTables(addressTables), minContextSlot,
	)
	if err != nil {
		return LegacySimulationEvidence{}, err
	}
	if simulation.ContextSlot < minContextSlot || !validSHA256(simulation.LogsSHA256) {
		return LegacySimulationEvidence{}, errors.New("v0 transaction simulation evidence is incomplete")
	}
	return LegacySimulationEvidence{
		ProviderIdentity: l.node.Identity(), MinContextSlot: minContextSlot,
		ContextSlot: simulation.ContextSlot, UnitsConsumed: simulation.UnitsConsumed,
		LogsSHA256: simulation.LogsSHA256,
	}, nil
}

func validateV0Inputs(
	message []byte,
	addressTables map[[32]byte][][32]byte,
	expectedSigner string,
	minContextSlot uint64,
) error {
	if minContextSlot == 0 {
		return errors.New("minimum v0 context slot is required")
	}
	decoded, err := solana.DecodeV0Message(message, addressTables)
	if err != nil {
		return errors.New("v0 message is invalid")
	}
	if err := solana.ValidateV0MessageForSigner(decoded, expectedSigner); err != nil {
		return errors.New("v0 signer shape is invalid")
	}
	return nil
}

func cloneAddressTables(tables map[[32]byte][][32]byte) map[[32]byte][][32]byte {
	cloned := make(map[[32]byte][][32]byte, len(tables))
	for table, addresses := range tables {
		cloned[table] = append([][32]byte(nil), addresses...)
	}
	return cloned
}
