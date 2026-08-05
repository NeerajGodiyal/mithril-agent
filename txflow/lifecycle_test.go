package txflow

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/orcaswap"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
)

type fakeProvider struct {
	identity          string
	send              string
	sendErr           error
	status            solanarpc.SignatureStatus
	statusErr         error
	height            uint64
	heightErr         error
	sendCalls         int
	genesis           string
	genesisErr        error
	fee               uint64
	feeErr            error
	feeSlot           uint64
	balance           uint64
	balanceErr        error
	balanceSlot       uint64
	finalized         uint64
	accountOwner      string
	accountExecutable bool
	accountDataLength uint64
	accountSliceErr   error
	accountSlices     map[string]solanarpc.AccountDataSlice
	rent              uint64
	rentErr           error
	simulation        solanarpc.Simulation
	simulateErr       error
	effect            solanarpc.TransactionEffect
	effectErr         error
	sendMinSlot       uint64
}

func (f *fakeProvider) Identity() string { return f.identity }

func (f *fakeProvider) GenesisHash(context.Context) (string, error) {
	return f.genesis, f.genesisErr
}

func (f *fakeProvider) FinalizedSlot(context.Context) (uint64, error) {
	return f.finalized, nil
}

func (f *fakeProvider) Account(
	_ context.Context,
	_ string,
	minContextSlot uint64,
) (solanarpc.AccountQuote, error) {
	slot := f.balanceSlot
	if slot == 0 {
		slot = minContextSlot
	}
	owner := f.accountOwner
	if owner == "" {
		owner = solana.Encode(make([]byte, 32))
	}
	return solanarpc.AccountQuote{
		ContextSlot: slot,
		Lamports:    f.balance,
		Owner:       owner,
		Executable:  f.accountExecutable,
		DataLength:  f.accountDataLength,
	}, f.balanceErr
}

func (f *fakeProvider) AccountSlice(
	_ context.Context,
	address string,
	minContextSlot,
	_,
	length uint64,
) (solanarpc.AccountDataSlice, error) {
	if f.accountSliceErr != nil {
		return solanarpc.AccountDataSlice{}, f.accountSliceErr
	}
	if value, ok := f.accountSlices[address]; ok {
		if value.ContextSlot == 0 {
			value.ContextSlot = minContextSlot
		}
		return value, nil
	}
	data := make([]byte, length)
	switch address {
	case orcaswap.WhirlpoolProgram:
		binary.LittleEndian.PutUint32(data[:4], 2)
		programData, _ := solana.Decode32(orcaswap.WhirlpoolProgramData)
		copy(data[4:], programData[:])
	case orcaswap.WhirlpoolProgramData:
		binary.LittleEndian.PutUint32(data[:4], 3)
		binary.LittleEndian.PutUint64(data[4:12], orcaswap.WhirlpoolDeploySlot)
		data[12] = 1
		authority, _ := solana.Decode32(orcaswap.WhirlpoolUpgradeAuth)
		copy(data[13:], authority[:])
	}
	return solanarpc.AccountDataSlice{
		ContextSlot: minContextSlot,
		Owner:       orcaswap.UpgradeableLoader,
		Executable:  address == orcaswap.WhirlpoolProgram,
		Data:        data,
	}, nil
}

func (f *fakeProvider) FeeForMessage(_ context.Context, _ []byte, minContextSlot uint64) (solanarpc.FeeQuote, error) {
	slot := f.feeSlot
	if slot == 0 {
		slot = minContextSlot
	}
	return solanarpc.FeeQuote{ContextSlot: slot, Lamports: f.fee}, f.feeErr
}

func (f *fakeProvider) MinimumBalanceForRentExemption(context.Context, uint64) (uint64, error) {
	if f.rent == 0 && f.rentErr == nil {
		return 2_039_280, nil
	}
	return f.rent, f.rentErr
}

func (f *fakeProvider) SimulateTransfer(_ context.Context, _ []byte, minContextSlot uint64) (solanarpc.Simulation, error) {
	simulation := f.simulation
	if simulation.ContextSlot == 0 {
		simulation.ContextSlot = minContextSlot
	}
	return simulation, f.simulateErr
}

func (f *fakeProvider) SendTransaction(
	_ context.Context,
	_ []byte,
	minContextSlot uint64,
) (string, error) {
	f.sendCalls++
	f.sendMinSlot = minContextSlot
	return f.send, f.sendErr
}

func (f *fakeProvider) SignatureStatus(context.Context, string) (solanarpc.SignatureStatus, error) {
	return f.status, f.statusErr
}

func (f *fakeProvider) TransactionEffect(context.Context, string) (solanarpc.TransactionEffect, error) {
	return f.effect, f.effectErr
}

func (f *fakeProvider) BlockHeight(context.Context) (uint64, error) {
	return f.height, f.heightErr
}

func TestVerifyWhirlpoolDeployment(t *testing.T) {
	policy := swapPolicy("3qbR1eZRqXUWroWKKYhbDmR3FfqTHfqSU8zZSxtANzYh")
	valid := func() map[string]solanarpc.AccountDataSlice {
		provider := &fakeProvider{}
		program, err := provider.AccountSlice(
			t.Context(), orcaswap.WhirlpoolProgram, 100, 0, 36,
		)
		if err != nil {
			t.Fatal(err)
		}
		programData, err := provider.AccountSlice(
			t.Context(), orcaswap.WhirlpoolProgramData, 100, 0, 45,
		)
		if err != nil {
			t.Fatal(err)
		}
		return map[string]solanarpc.AccountDataSlice{
			orcaswap.WhirlpoolProgram:     program,
			orcaswap.WhirlpoolProgramData: programData,
		}
	}
	clone := func(input map[string]solanarpc.AccountDataSlice) map[string]solanarpc.AccountDataSlice {
		output := make(map[string]solanarpc.AccountDataSlice, len(input))
		for key, value := range input {
			value.Data = bytes.Clone(value.Data)
			output[key] = value
		}
		return output
	}

	primary := &fakeProvider{identity: "primary", accountSlices: valid()}
	secondary := &fakeProvider{identity: "secondary", accountSlices: valid()}
	lifecycle, err := New(&fakeProvider{identity: "node"}, primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.VerifyWhirlpoolDeployment(t.Context(), policy, 100); err != nil {
		t.Fatal(err)
	}
	buy := orcaswap.BuyPolicyV2{
		Owner: policy.Owner, Pool: orcaswap.DevnetPool,
		TokenMintA: orcaswap.WrappedSOLMint, TokenMintB: orcaswap.DevnetUSDCMint,
		InputTokenAccount: policy.OutputTokenAccount,
		TokenVaultA:       policy.TokenVaultA, TokenVaultB: policy.TokenVaultB,
		Oracle: policy.Oracle, ProgramData: policy.ProgramData,
		UpgradeAuthority: policy.UpgradeAuthority, DeploymentSlot: policy.DeploymentSlot,
		MaxInputTokenAmount: 1_000, MinOutputLamports: 1,
		MaxSlippageBPS: 100, MaxTemporaryRentLamports: 3_000_000,
	}
	if err := lifecycle.VerifyWhirlpoolBuyDeployment(t.Context(), buy, 100); err != nil {
		t.Fatal(err)
	}

	tests := map[string]func(map[string]solanarpc.AccountDataSlice, map[string]solanarpc.AccountDataSlice){
		"provider mismatch": func(first, _ map[string]solanarpc.AccountDataSlice) {
			value := first[orcaswap.WhirlpoolProgram]
			value.Data[4] ^= 1
		},
		"stale context": func(first, second map[string]solanarpc.AccountDataSlice) {
			for _, values := range []map[string]solanarpc.AccountDataSlice{first, second} {
				value := values[orcaswap.WhirlpoolProgram]
				value.ContextSlot = 99
				values[orcaswap.WhirlpoolProgram] = value
			}
		},
		"wrong owner": func(first, second map[string]solanarpc.AccountDataSlice) {
			for _, values := range []map[string]solanarpc.AccountDataSlice{first, second} {
				value := values[orcaswap.WhirlpoolProgram]
				value.Owner = solana.Encode(make([]byte, 32))
				values[orcaswap.WhirlpoolProgram] = value
			}
		},
		"program not executable": func(first, second map[string]solanarpc.AccountDataSlice) {
			for _, values := range []map[string]solanarpc.AccountDataSlice{first, second} {
				value := values[orcaswap.WhirlpoolProgram]
				value.Executable = false
				values[orcaswap.WhirlpoolProgram] = value
			}
		},
		"wrong program discriminant": func(first, second map[string]solanarpc.AccountDataSlice) {
			for _, values := range []map[string]solanarpc.AccountDataSlice{first, second} {
				binary.LittleEndian.PutUint32(values[orcaswap.WhirlpoolProgram].Data[:4], 1)
			}
		},
		"wrong program-data link": func(first, second map[string]solanarpc.AccountDataSlice) {
			for _, values := range []map[string]solanarpc.AccountDataSlice{first, second} {
				values[orcaswap.WhirlpoolProgram].Data[4] ^= 1
			}
		},
		"short program header": func(first, second map[string]solanarpc.AccountDataSlice) {
			for _, values := range []map[string]solanarpc.AccountDataSlice{first, second} {
				value := values[orcaswap.WhirlpoolProgram]
				value.Data = value.Data[:35]
				values[orcaswap.WhirlpoolProgram] = value
			}
		},
		"wrong program-data discriminant": func(first, second map[string]solanarpc.AccountDataSlice) {
			for _, values := range []map[string]solanarpc.AccountDataSlice{first, second} {
				binary.LittleEndian.PutUint32(values[orcaswap.WhirlpoolProgramData].Data[:4], 2)
			}
		},
		"wrong deployment slot": func(first, second map[string]solanarpc.AccountDataSlice) {
			for _, values := range []map[string]solanarpc.AccountDataSlice{first, second} {
				binary.LittleEndian.PutUint64(values[orcaswap.WhirlpoolProgramData].Data[4:12], 1)
			}
		},
		"missing upgrade authority": func(first, second map[string]solanarpc.AccountDataSlice) {
			for _, values := range []map[string]solanarpc.AccountDataSlice{first, second} {
				values[orcaswap.WhirlpoolProgramData].Data[12] = 0
			}
		},
		"wrong upgrade authority": func(first, second map[string]solanarpc.AccountDataSlice) {
			for _, values := range []map[string]solanarpc.AccountDataSlice{first, second} {
				values[orcaswap.WhirlpoolProgramData].Data[13] ^= 1
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			first, second := clone(valid()), clone(valid())
			mutate(first, second)
			candidate, err := New(
				&fakeProvider{identity: "node"},
				&fakeProvider{identity: "primary", accountSlices: first},
				&fakeProvider{identity: "secondary", accountSlices: second},
			)
			if err != nil {
				t.Fatal(err)
			}
			if err := candidate.VerifyWhirlpoolDeployment(t.Context(), policy, 100); err == nil {
				t.Fatal("invalid Whirlpool deployment was accepted")
			}
		})
	}

	failing, err := New(
		&fakeProvider{identity: "node"},
		&fakeProvider{identity: "primary", accountSliceErr: errors.New("unavailable")},
		&fakeProvider{identity: "secondary"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := failing.VerifyWhirlpoolDeployment(t.Context(), policy, 100); err == nil {
		t.Fatal("provider failure was accepted")
	}
	if err := lifecycle.VerifyWhirlpoolDeployment(t.Context(), policy, 0); err == nil {
		t.Fatal("zero deployment context was accepted")
	}
}

func TestVerifyTokenInputAccountRequiresIndependentIdentityAndBalance(t *testing.T) {
	owner := solana.Encode(bytes.Repeat([]byte{4}, 32))
	mint := solana.Encode(bytes.Repeat([]byte{5}, 32))
	address := solana.Encode(bytes.Repeat([]byte{6}, 32))
	data := make([]byte, 165)
	copy(data[:32], bytes.Repeat([]byte{5}, 32))
	copy(data[32:64], bytes.Repeat([]byte{4}, 32))
	binary.LittleEndian.PutUint64(data[64:], 1_000)
	data[108] = 1
	slice := solanarpc.AccountDataSlice{
		ContextSlot: 100, Owner: orcaswap.TokenProgram, DataLength: 165, Data: data,
	}
	primary := &fakeProvider{identity: "a", accountSlices: map[string]solanarpc.AccountDataSlice{address: slice}}
	secondary := &fakeProvider{identity: "b", accountSlices: map[string]solanarpc.AccountDataSlice{address: slice}}
	lifecycle, err := New(&fakeProvider{identity: "node"}, primary, secondary)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := lifecycle.VerifyTokenInputAccount(
		t.Context(), address, mint, owner, 1_000, 100,
	)
	if err != nil || evidence.Amount != 1_000 || evidence.PrimaryContextSlot != 100 ||
		evidence.SecondaryContextSlot != 100 {
		t.Fatalf("evidence=%+v error=%v", evidence, err)
	}

	tests := map[string]func(*solanarpc.AccountDataSlice, *solanarpc.AccountDataSlice){
		"provider disagreement": func(_, second *solanarpc.AccountDataSlice) {
			second.Data[71]++
		},
		"wrong account size": func(first, second *solanarpc.AccountDataSlice) {
			first.DataLength, second.DataLength = 166, 166
		},
		"wrong program": func(first, second *solanarpc.AccountDataSlice) {
			first.Owner, second.Owner = owner, owner
		},
		"executable": func(first, second *solanarpc.AccountDataSlice) {
			first.Executable, second.Executable = true, true
		},
		"wrong mint": func(first, second *solanarpc.AccountDataSlice) {
			first.Data[0]++
			second.Data[0]++
		},
		"wrong owner": func(first, second *solanarpc.AccountDataSlice) {
			first.Data[32]++
			second.Data[32]++
		},
		"uninitialized": func(first, second *solanarpc.AccountDataSlice) {
			first.Data[108], second.Data[108] = 0, 0
		},
		"frozen": func(first, second *solanarpc.AccountDataSlice) {
			first.Data[108], second.Data[108] = 2, 2
		},
		"native token account": func(first, second *solanarpc.AccountDataSlice) {
			binary.LittleEndian.PutUint32(first.Data[109:113], 1)
			binary.LittleEndian.PutUint32(second.Data[109:113], 1)
		},
		"insufficient": func(first, second *solanarpc.AccountDataSlice) {
			binary.LittleEndian.PutUint64(first.Data[64:], 999)
			binary.LittleEndian.PutUint64(second.Data[64:], 999)
		},
		"stale": func(first, second *solanarpc.AccountDataSlice) {
			first.ContextSlot, second.ContextSlot = 99, 99
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			first, second := slice, slice
			first.Data, second.Data = bytes.Clone(data), bytes.Clone(data)
			mutate(&first, &second)
			primary.accountSlices[address], secondary.accountSlices[address] = first, second
			if _, err := lifecycle.VerifyTokenInputAccount(
				t.Context(), address, mint, owner, 1_000, 100,
			); err == nil {
				t.Fatal("invalid token account evidence was accepted")
			}
		})
	}
}

func TestVerifyTokenAccountRent(t *testing.T) {
	newLifecycle := func(primary, secondary *fakeProvider) *Lifecycle {
		lifecycle, err := New(&fakeProvider{identity: "node"}, primary, secondary)
		if err != nil {
			t.Fatal(err)
		}
		return lifecycle
	}
	valid := newLifecycle(
		&fakeProvider{identity: "primary", rent: 2_039_280},
		&fakeProvider{identity: "secondary", rent: 2_039_280},
	)
	evidence, err := valid.VerifyTokenAccountRent(t.Context(), 3_000_000)
	if err != nil || evidence.Lamports != 2_039_280 ||
		evidence.PrimaryLamports != evidence.SecondaryLamports {
		t.Fatalf("rent evidence = %+v, %v", evidence, err)
	}
	for name, providers := range map[string][2]*fakeProvider{
		"disagreement": {
			{identity: "primary", rent: 2_039_280},
			{identity: "secondary", rent: 2_039_281},
		},
		"over cap": {
			{identity: "primary", rent: 3_000_001},
			{identity: "secondary", rent: 3_000_001},
		},
		"provider error": {
			{identity: "primary", rentErr: errors.New("unavailable")},
			{identity: "secondary", rent: 2_039_280},
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := newLifecycle(providers[0], providers[1]).VerifyTokenAccountRent(
				t.Context(), 3_000_000,
			); err == nil {
				t.Fatal("invalid rent evidence was accepted")
			}
		})
	}
	if _, err := valid.VerifyTokenAccountRent(t.Context(), 0); err == nil {
		t.Fatal("zero rent cap was accepted")
	}
}

func TestSubmitNeverRetriesAmbiguousSend(t *testing.T) {
	transaction, signature := signedTransfer(t)
	node := &fakeProvider{identity: "node", sendErr: errors.New("timeout")}
	lifecycle, err := New(
		node,
		&fakeProvider{identity: "primary"},
		&fakeProvider{identity: "secondary"},
	)
	if err != nil {
		t.Fatal(err)
	}
	submission, err := lifecycle.Submit(t.Context(), transaction, 200, 150)
	if err != nil {
		t.Fatal(err)
	}
	if submission.State != StateAmbiguous || submission.Signature != signature ||
		node.sendCalls != 1 || node.sendMinSlot != 150 {
		t.Fatalf("unexpected ambiguous submission: %+v calls=%d", submission, node.sendCalls)
	}
}

func TestSubmitRequiresExpectedSignature(t *testing.T) {
	transaction, _ := signedTransfer(t)
	node := &fakeProvider{identity: "node", send: solana.Encode(bytes.Repeat([]byte{4}, 64))}
	lifecycle, err := New(
		node,
		&fakeProvider{identity: "primary"},
		&fakeProvider{identity: "secondary"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Submit(t.Context(), transaction, 200, 150); err == nil {
		t.Fatal("mismatched RPC signature was accepted")
	}
}

func TestReconcileIndependentVerdicts(t *testing.T) {
	transaction, signature := signedTransfer(t)
	base := Submission{Signature: signature, LastValidBlockHeight: 200, State: StateAmbiguous}
	finalized := solanarpc.SignatureStatus{Found: true, Slot: 150, ConfirmationStatus: "finalized"}
	failed := solanarpc.SignatureStatus{
		Found:              true,
		Slot:               150,
		ConfirmationStatus: "finalized",
		Failed:             true,
		ErrorFingerprint:   "failure",
	}
	tests := map[string]struct {
		first, second *fakeProvider
		want          string
	}{
		"finalized": {
			&fakeProvider{identity: "a", status: finalized, effect: transferEffect(transaction, false)},
			&fakeProvider{identity: "b", status: finalized, effect: transferEffect(transaction, false)},
			VerdictFinalized,
		},
		"failed": {
			&fakeProvider{identity: "a", status: failed, effect: transferEffect(transaction, true)},
			&fakeProvider{identity: "b", status: failed, effect: transferEffect(transaction, true)},
			VerdictFailed,
		},
		"one source": {
			&fakeProvider{identity: "a", status: finalized, height: 199},
			&fakeProvider{identity: "b", height: 199},
			VerdictPending,
		},
		"one source after expiry": {
			&fakeProvider{identity: "a", status: finalized, height: 201},
			&fakeProvider{identity: "b", height: 202},
			VerdictUnresolved,
		},
		"processed failure": {
			&fakeProvider{identity: "a", status: solanarpc.SignatureStatus{
				Found: true, Slot: 150, ConfirmationStatus: "processed", Failed: true, ErrorFingerprint: "failure",
			}, height: 199},
			&fakeProvider{identity: "b", status: solanarpc.SignatureStatus{
				Found: true, Slot: 150, ConfirmationStatus: "processed", Failed: true, ErrorFingerprint: "failure",
			}, height: 199},
			VerdictPending,
		},
		"confirmed is not terminal": {
			&fakeProvider{identity: "a", status: solanarpc.SignatureStatus{
				Found: true, Slot: 150, ConfirmationStatus: "confirmed",
			}, height: 201},
			&fakeProvider{identity: "b", status: solanarpc.SignatureStatus{
				Found: true, Slot: 150, ConfirmationStatus: "confirmed",
			}, height: 202},
			VerdictPending,
		},
		"disagreement": {
			&fakeProvider{identity: "a", status: finalized},
			&fakeProvider{identity: "b", status: solanarpc.SignatureStatus{Found: true, Slot: 151, ConfirmationStatus: "finalized"}},
			VerdictDiverged,
		},
		"expired": {
			&fakeProvider{identity: "a", height: 201},
			&fakeProvider{identity: "b", height: 202},
			VerdictUnresolved,
		},
		"not expired": {
			&fakeProvider{identity: "a", height: 201},
			&fakeProvider{identity: "b", height: 200},
			VerdictPending,
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			lifecycle, err := New(
				&fakeProvider{identity: "node"},
				test.first,
				test.second,
			)
			if err != nil {
				t.Fatal(err)
			}
			result, err := lifecycle.Reconcile(t.Context(), base, transaction, 5)
			if err != nil {
				t.Fatal(err)
			}
			if result.Verdict != test.want {
				t.Fatalf("verdict = %s, want %s", result.Verdict, test.want)
			}
			if (test.want == VerdictFinalized || test.want == VerdictFailed) && result.Effects == nil {
				t.Fatal("terminal reconciliation omitted exact effects")
			}
		})
	}
}

func TestSubmitTreatsRPCRejectionAsAmbiguous(t *testing.T) {
	transaction, signature := signedTransfer(t)
	node := &fakeProvider{identity: "node", sendErr: &solanarpc.RPCError{Code: -32002}}
	lifecycle, err := New(
		node,
		&fakeProvider{identity: "primary"},
		&fakeProvider{identity: "secondary"},
	)
	if err != nil {
		t.Fatal(err)
	}
	submission, err := lifecycle.Submit(t.Context(), transaction, 200, 150)
	if err != nil {
		t.Fatal(err)
	}
	if submission.State != StateAmbiguous || submission.Signature != signature {
		t.Fatalf("ambiguous submission = %+v", submission)
	}
}

func TestBlockhashExpiredUsesMithrilProcessedHeight(t *testing.T) {
	node := &fakeProvider{identity: "node", height: 200}
	lifecycle, err := New(
		node,
		&fakeProvider{identity: "a", height: 300},
		&fakeProvider{identity: "b", height: 300},
	)
	if err != nil {
		t.Fatal(err)
	}
	expired, err := lifecycle.BlockhashExpired(t.Context(), 200)
	if err != nil {
		t.Fatal(err)
	}
	if expired {
		t.Fatal("current Mithril height declared the blockhash expired")
	}
	node.height = 201
	expired, err = lifecycle.BlockhashExpired(t.Context(), 200)
	if err != nil || !expired {
		t.Fatalf("Mithril expiry = %v, %v", expired, err)
	}
}

func TestNewRequiresIndependentProviders(t *testing.T) {
	if _, err := New(
		&fakeProvider{identity: "node"},
		&fakeProvider{identity: "same"},
		&fakeProvider{identity: "same"},
	); err == nil {
		t.Fatal("same provider identity was accepted")
	}
	if _, err := New(
		&fakeProvider{identity: "same"},
		&fakeProvider{identity: "same"},
		&fakeProvider{identity: "secondary"},
	); err == nil {
		t.Fatal("Mithril and evidence provider identity collision was accepted")
	}
}

func TestIndependentQueriesRespectContextWhenProviderDoesNotReturn(t *testing.T) {
	blocked := make(chan struct{})
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, _, err := queryPair(
		ctx,
		func(context.Context) (uint64, error) {
			<-blocked
			return 1, nil
		},
		func(context.Context) (uint64, error) {
			return 2, nil
		},
	)
	close(blocked)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("query error = %v", err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("query ignored its context deadline")
	}
}

func TestIndependentQueriesJoinPeerAfterProviderError(t *testing.T) {
	peerStarted := make(chan struct{})
	peerDone := make(chan struct{})
	_, _, err := queryPair(
		t.Context(),
		func(context.Context) (uint64, error) {
			<-peerStarted
			return 0, errors.New("unavailable")
		},
		func(ctx context.Context) (uint64, error) {
			close(peerStarted)
			<-ctx.Done()
			close(peerDone)
			return 0, ctx.Err()
		},
	)
	if err == nil {
		t.Fatal("provider error was accepted")
	}
	select {
	case <-peerDone:
	default:
		t.Fatal("query returned before the peer stopped")
	}
}

func TestVerifyGenesisRequiresBothProviders(t *testing.T) {
	first := &fakeProvider{identity: "a", genesis: solana.DevnetGenesisHash}
	second := &fakeProvider{identity: "b", genesis: solana.DevnetGenesisHash}
	node := &fakeProvider{identity: "node", genesis: solana.DevnetGenesisHash}
	lifecycle, err := New(node, first, second)
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.VerifyGenesis(t.Context(), solana.DevnetGenesisHash); err != nil {
		t.Fatal(err)
	}
	node.genesis = solana.Encode(bytes.Repeat([]byte{8}, 32))
	if err := lifecycle.VerifyEvidenceGenesis(t.Context(), solana.DevnetGenesisHash); err != nil {
		t.Fatalf("independent evidence check required the local node: %v", err)
	}
	if err := lifecycle.VerifyGenesis(t.Context(), solana.DevnetGenesisHash); err == nil {
		t.Fatal("wrong local node genesis hash was accepted for a new action")
	}
	second.genesis = solana.Encode(bytes.Repeat([]byte{9}, 32))
	if err := lifecycle.VerifyEvidenceGenesis(t.Context(), solana.DevnetGenesisHash); err == nil {
		t.Fatal("wrong secondary genesis hash was accepted")
	}
}

func TestVerifyGenesisExplainsUnavailableNode(t *testing.T) {
	node := &fakeProvider{identity: "node", genesisErr: errors.New("unavailable")}
	lifecycle, err := New(
		node,
		&fakeProvider{identity: "a", genesis: solana.DevnetGenesisHash},
		&fakeProvider{identity: "b", genesis: solana.DevnetGenesisHash},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.VerifyGenesis(t.Context(), solana.DevnetGenesisHash); err == nil ||
		err.Error() != "Mithril node RPC is unavailable or not ready" {
		t.Fatalf("error = %v", err)
	}
}

func TestFeeForMessageRequiresMatchingProviders(t *testing.T) {
	transaction, _ := signedTransfer(t)
	decoded, err := solana.DecodeSignedTransfer(transaction)
	if err != nil {
		t.Fatal(err)
	}
	first := &fakeProvider{identity: "a", fee: 5_000}
	second := &fakeProvider{identity: "b", fee: 5_000}
	lifecycle, err := New(&fakeProvider{identity: "node"}, first, second)
	if err != nil {
		t.Fatal(err)
	}
	fee, err := lifecycle.FeeForMessage(t.Context(), decoded.Message, 90)
	if err != nil || fee.Lamports != 5_000 || fee.MinContextSlot != 90 {
		t.Fatalf("fee = %+v, %v", fee, err)
	}
	second.fee = 6_000
	if _, err := lifecycle.FeeForMessage(t.Context(), decoded.Message, 90); err == nil {
		t.Fatal("provider fee disagreement was accepted")
	}
	second.fee = 0
	if _, err := lifecycle.FeeForMessage(t.Context(), decoded.Message, 90); err == nil {
		t.Fatal("zero provider fee was accepted")
	}
	second.fee = 5_000
	second.feeSlot = 89
	if _, err := lifecycle.FeeForMessage(t.Context(), decoded.Message, 90); err == nil {
		t.Fatal("stale provider fee context was accepted")
	}
}

func TestAccountsForTransferUseFreshIndependentEvidence(t *testing.T) {
	source := solana.Encode(bytes.Repeat([]byte{8}, 32))
	destination := solana.Encode(bytes.Repeat([]byte{9}, 32))
	first := &fakeProvider{identity: "a", balance: 1_000, balanceSlot: 91, finalized: 90}
	second := &fakeProvider{identity: "b", balance: 1_000, balanceSlot: 92, finalized: 93}
	lifecycle, err := New(&fakeProvider{identity: "node"}, first, second)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := lifecycle.AccountsForTransfer(t.Context(), source, destination, 95)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.ObservationSlot != 95 ||
		evidence.CommonFinalizedFloor != 90 ||
		evidence.PrimaryFinalizedSlot != 90 ||
		evidence.SecondaryFinalizedSlot != 93 ||
		evidence.Source.Address != source ||
		evidence.Destination.Address != destination ||
		evidence.Source.PrimaryLamports != 1_000 ||
		evidence.Source.SecondaryLamports != 1_000 ||
		evidence.Source.PrimaryContextSlot != 91 ||
		evidence.Source.SecondaryContextSlot != 92 {
		t.Fatalf("account evidence = %+v", evidence)
	}

	second.balance = 900
	if _, err := lifecycle.AccountsForTransfer(t.Context(), source, destination, 95); err == nil {
		t.Fatal("provider account disagreement was accepted")
	}
	second.balance = 1_000
	second.balanceSlot = 89
	if _, err := lifecycle.AccountsForTransfer(t.Context(), source, destination, 95); err == nil {
		t.Fatal("stale secondary account was accepted")
	}
	second.balanceSlot = 92
	second.balanceErr = errors.New("unavailable")
	if _, err := lifecycle.AccountsForTransfer(t.Context(), source, destination, 95); err == nil {
		t.Fatal("missing secondary account was accepted")
	}
	second.balanceErr = nil
	nonSystemOwner := solana.Encode(bytes.Repeat([]byte{7}, 32))
	first.accountOwner = nonSystemOwner
	second.accountOwner = nonSystemOwner
	if _, err := lifecycle.AccountsForTransfer(t.Context(), source, destination, 95); err == nil {
		t.Fatal("non-System account was accepted")
	}
	first.accountOwner = ""
	second.accountOwner = ""
	first.accountExecutable = true
	second.accountExecutable = true
	if _, err := lifecycle.AccountsForTransfer(t.Context(), source, destination, 95); err == nil {
		t.Fatal("executable account was accepted")
	}
	first.accountExecutable = false
	second.accountExecutable = false
	first.accountDataLength = 1
	second.accountDataLength = 1
	if _, err := lifecycle.AccountsForTransfer(t.Context(), source, destination, 95); err == nil {
		t.Fatal("account with data was accepted")
	}
}

func TestSimulateUsesPrimaryProviderAndFreshContext(t *testing.T) {
	first := &fakeProvider{
		identity: "a",
		simulation: solanarpc.Simulation{
			ContextSlot:             91,
			UnitsConsumed:           150,
			SourcePostLamports:      900,
			DestinationPostLamports: 50,
			LogsSHA256:              hex.EncodeToString(bytes.Repeat([]byte{1}, sha256.Size)),
			AccountsSHA256:          hex.EncodeToString(bytes.Repeat([]byte{2}, sha256.Size)),
		},
	}
	lifecycle, err := New(
		first,
		&fakeProvider{identity: "primary"},
		&fakeProvider{identity: "secondary"},
	)
	if err != nil {
		t.Fatal(err)
	}
	evidence, err := lifecycle.Simulate(t.Context(), []byte("message"), 90)
	if err != nil {
		t.Fatal(err)
	}
	if evidence.ProviderIdentity != "a" || evidence.MinContextSlot != 90 ||
		evidence.ContextSlot != 91 || evidence.UnitsConsumed != 150 {
		t.Fatalf("simulation evidence = %+v", evidence)
	}
	first.simulation.SourcePostLamports = 0
	if _, err := lifecycle.Simulate(t.Context(), []byte("message"), 90); err != nil {
		t.Fatalf("valid zero-reserve simulation rejected: %v", err)
	}
	first.simulation.SourcePostLamports = 900
	first.simulation.DestinationPostLamports = 0
	if _, err := lifecycle.Simulate(t.Context(), []byte("message"), 90); err == nil {
		t.Fatal("zero destination balance was accepted after a positive transfer")
	}
	first.simulation.DestinationPostLamports = 50
	first.simulation.ContextSlot = 89
	if _, err := lifecycle.Simulate(t.Context(), []byte("message"), 90); err == nil {
		t.Fatal("stale simulation was accepted")
	}
}

func TestReconcileRejectsEffectMismatch(t *testing.T) {
	transaction, signature := signedTransfer(t)
	status := solanarpc.SignatureStatus{
		Found:              true,
		Slot:               150,
		ConfirmationStatus: "finalized",
	}
	first := &fakeProvider{
		identity: "a",
		status:   status,
		effect:   transferEffect(transaction, false),
	}
	second := &fakeProvider{
		identity: "b",
		status:   status,
		effect:   transferEffect(transaction, false),
	}
	lifecycle, err := New(&fakeProvider{identity: "node"}, first, second)
	if err != nil {
		t.Fatal(err)
	}
	submission := Submission{Signature: signature, LastValidBlockHeight: 200, State: StateAccepted}
	second.effect.PostBalances[1]++
	result, err := lifecycle.Reconcile(t.Context(), submission, transaction, 5)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != VerdictDiverged || result.DivergenceKind != DivergenceEffects {
		t.Fatalf("provider effect disagreement = %+v", result)
	}
	second.effect = transferEffect(transaction, false)
	second.effect.PostBalances[0]++
	first.effect.PostBalances[0]++
	result, err = lifecycle.Reconcile(t.Context(), submission, transaction, 5)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != VerdictDiverged || result.DivergenceKind != DivergenceEffects {
		t.Fatalf("matching but incorrect effects = %+v", result)
	}
}

func TestReconcileRejectsStatusAndTransactionErrorMismatch(t *testing.T) {
	transaction, signature := signedTransfer(t)
	status := solanarpc.SignatureStatus{
		Found:              true,
		Slot:               150,
		ConfirmationStatus: "finalized",
		Failed:             true,
		ErrorFingerprint:   "status-error",
	}
	firstEffect := transferEffect(transaction, true)
	firstEffect.ErrorFingerprint = status.ErrorFingerprint
	secondEffect := firstEffect
	secondEffect.Transaction = bytes.Clone(firstEffect.Transaction)
	secondEffect.PreBalances = append([]uint64(nil), firstEffect.PreBalances...)
	secondEffect.PostBalances = append([]uint64(nil), firstEffect.PostBalances...)
	secondEffect.ErrorFingerprint = "different-transaction-error"
	lifecycle, err := New(
		&fakeProvider{identity: "node"},
		&fakeProvider{identity: "a", status: status, effect: firstEffect},
		&fakeProvider{identity: "b", status: status, effect: secondEffect},
	)
	if err != nil {
		t.Fatal(err)
	}
	result, err := lifecycle.Reconcile(
		t.Context(),
		Submission{
			Signature:            signature,
			LastValidBlockHeight: 200,
			State:                StateAccepted,
		},
		transaction,
		5,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Verdict != VerdictDiverged ||
		result.DivergenceKind != DivergenceEffects {
		t.Fatalf("status/transaction error mismatch = %+v", result)
	}
}

func signedTransfer(t *testing.T) ([]byte, string) {
	t.Helper()
	seed := sha256.Sum256([]byte("source"))
	key := ed25519.NewKeyFromSeed(seed[:])
	source := solana.Encode(key.Public().(ed25519.PublicKey))
	destinationSeed := sha256.Sum256([]byte("destination"))
	destination := solana.Encode(ed25519.NewKeyFromSeed(destinationSeed[:]).Public().(ed25519.PublicKey))
	blockhash := solana.Encode(bytes.Repeat([]byte{3}, 32))
	message, err := solana.BuildTransferMessage(source, destination, blockhash, 1)
	if err != nil {
		t.Fatal(err)
	}
	transaction, signature, err := solana.SignTransferMessage(key, message)
	if err != nil {
		t.Fatal(err)
	}
	return transaction, solana.Encode(signature[:])
}

func transferEffect(transaction []byte, failed bool) solanarpc.TransactionEffect {
	post := []uint64{94, 21, 1}
	errorFingerprint := ""
	if failed {
		post = []uint64{95, 20, 1}
		errorFingerprint = "failure"
	}
	return solanarpc.TransactionEffect{
		Slot:             150,
		Transaction:      bytes.Clone(transaction),
		FeeLamports:      5,
		Failed:           failed,
		ErrorFingerprint: errorFingerprint,
		PreBalances:      []uint64{100, 20, 1},
		PostBalances:     post,
	}
}
