package txflow

import (
	"context"
	"errors"

	"github.com/Overclock-Validator/mithril-agent/solana"
)

// VerifyNativeReserve reads matching plain-System-account balances from the
// lifecycle's independent providers. The caller supplies the protected owner,
// checked upfront cost and inclusive context interval from its proposal recheck.
// This observation neither reserves funds nor authorizes a transaction.
func (l *Lifecycle) VerifyNativeReserve(
	ctx context.Context,
	owner string,
	upfrontLamports, reserveLamports, minimumSlot, maximumSlot uint64,
) (AccountEvidence, error) {
	if _, err := solana.Decode32(owner); err != nil || upfrontLamports == 0 ||
		reserveLamports == 0 || minimumSlot == 0 || maximumSlot < minimumSlot {
		return AccountEvidence{}, errors.New("native reserve check inputs are invalid")
	}
	evidence, err := l.accountEvidence(ctx, owner, minimumSlot)
	if err != nil {
		return AccountEvidence{}, err
	}
	if evidence.PrimaryContextSlot > maximumSlot || evidence.SecondaryContextSlot > maximumSlot {
		return AccountEvidence{}, errors.New("native balance context is too far from the checked proposal")
	}
	// Subtraction rejects overflow-sized combined requirements without wrapping.
	if evidence.PrimaryLamports < reserveLamports || evidence.PrimaryLamports-reserveLamports < upfrontLamports {
		return AccountEvidence{}, errors.New("native balance cannot cover checked upfront cost and retained reserve")
	}
	return evidence, nil
}
