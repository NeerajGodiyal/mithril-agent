package proposalcheck

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/txflow"
)

type nativeReserveFixture struct {
	*fakeEvidence
	calls                              int
	owner                              string
	upfront, reserve, minimum, maximum uint64
	mutate                             func(*txflow.AccountEvidence)
	err                                error
}

func newNativeReserveFixture() *nativeReserveFixture {
	return &nativeReserveFixture{fakeEvidence: &fakeEvidence{
		fee:         txflow.FeeEvidence{Lamports: 5000, PrimaryContextSlot: 110, SecondaryContextSlot: 111},
		simulation:  txflow.LegacySimulationEvidence{ContextSlot: 112, UnitsConsumed: 40_000, LogsSHA256: strings.Repeat("0", 64)},
		blockHeight: 101,
	}}
}

func (f *nativeReserveFixture) VerifyNativeReserve(_ context.Context, owner string, upfront, reserve, minimum, maximum uint64) (txflow.AccountEvidence, error) {
	f.calls++
	f.owner, f.upfront, f.reserve, f.minimum, f.maximum = owner, upfront, reserve, minimum, maximum
	account := txflow.AccountEvidence{Address: owner, PrimaryContextSlot: minimum, SecondaryContextSlot: minimum + 1,
		PrimaryLamports: upfront + reserve, SecondaryLamports: upfront + reserve,
		PrimaryOwner: "11111111111111111111111111111111", SecondaryOwner: "11111111111111111111111111111111"}
	if f.mutate != nil {
		f.mutate(&account)
	}
	return account, f.err
}

func TestNativeReserveRecheckBindsExactUnsignedProposal(t *testing.T) {
	candidate := candidateFixture(t)
	plainEvidence := newNativeReserveFixture()
	plain, err := Recheck(t.Context(), plainEvidence, primarySlot(110), secondarySlot(111), candidate.Policy, checkedProviderBindings(), candidate)
	if err != nil {
		t.Fatal(err)
	}
	if plainEvidence.calls != 0 {
		t.Fatal("ordinary recheck unexpectedly queried native reserve")
	}
	f := newNativeReserveFixture()
	got, err := RecheckWithNativeReserve(t.Context(), f, primarySlot(110), secondarySlot(111), candidate.Policy, checkedProviderBindings(), candidate, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got.Result, plain) || got.SigningEnabled || got.SubmissionEnabled || !got.NativeReserve.AdvisoryOnly ||
		f.calls != 1 || f.owner != candidate.Policy.Owner || f.upfront != plain.MaximumUpfrontLamports || f.reserve != 1000 ||
		f.minimum != 112 || f.maximum != 260 || got.NativeReserve.ObservedBalanceLamports != plain.MaximumUpfrontLamports+1000 {
		t.Fatalf("reserve review differs from exact recheck: %+v, %+v", got, f)
	}
	encoded, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("native_reserve")) {
		t.Fatal("ordinary result serialization changed")
	}
	f = newNativeReserveFixture()
	f.mutate = func(account *txflow.AccountEvidence) {
		account.PrimaryLamports = 9007199254740993
		account.SecondaryLamports = account.PrimaryLamports
	}
	got, err = RecheckWithNativeReserve(t.Context(), f, primarySlot(110), secondarySlot(111), candidate.Policy, checkedProviderBindings(), candidate, 1000)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(got)
	if err != nil || !bytes.Contains(encoded, []byte(`"observed_balance_lamports":"9007199254740993"`)) {
		t.Fatalf("balance lost JSON precision: %s, %v", encoded, err)
	}
}

func TestNativeReserveRecheckRejectsUnboundEvidence(t *testing.T) {
	for _, name := range []string{"address", "owner", "disagreement", "stale", "ahead", "executable", "data", "insufficient", "underflow", "overflow", "reader error", "providers", "candidate", "zero reserve", "context overflow"} {
		t.Run(name, func(t *testing.T) {
			candidate := candidateFixture(t)
			f := newNativeReserveFixture()
			bindings := checkedProviderBindings()
			reserve, slot := uint64(1000), uint64(110)
			f.mutate = func(a *txflow.AccountEvidence) {
				switch name {
				case "address":
					a.Address = "wrong"
				case "owner":
					a.PrimaryOwner = "wrong"
					a.SecondaryOwner = "wrong"
				case "disagreement":
					a.SecondaryLamports--
				case "stale":
					a.PrimaryContextSlot--
				case "ahead":
					a.SecondaryContextSlot = 261
				case "executable":
					a.PrimaryExecutable = true
				case "data":
					a.SecondaryDataLength = 1
				case "insufficient":
					a.PrimaryLamports--
					a.SecondaryLamports--
				case "underflow":
					a.PrimaryLamports = 999
					a.SecondaryLamports = 999
				case "overflow":
					a.PrimaryLamports = ^uint64(0)
					a.SecondaryLamports = a.PrimaryLamports
				}
			}
			switch name {
			case "reader error":
				f.err = errors.New("offline injected failure")
			case "providers":
				bindings.PrimaryOriginSHA256 = strings.Repeat("f", 64)
			case "candidate":
				candidate.Request.InputAmount++
			case "zero reserve":
				reserve = 0
			case "overflow":
				reserve = ^uint64(0)
			case "context overflow":
				slot = ^uint64(0) - 10
				f.fee.PrimaryContextSlot, f.fee.SecondaryContextSlot = slot, slot
				f.simulation.ContextSlot = slot
			}
			got, err := RecheckWithNativeReserve(t.Context(), f, primarySlot(slot), secondarySlot(slot), candidate.Policy, bindings, candidate, reserve)
			if err == nil || !reflect.DeepEqual(got, NativeReserveResult{}) {
				t.Fatalf("unbound reserve accepted: %+v, %v", got, err)
			}
			if (name == "providers" || name == "candidate" || name == "zero reserve" || name == "context overflow") && f.calls != 0 {
				t.Fatal("native balance read preceded existing validation")
			}
		})
	}
}
