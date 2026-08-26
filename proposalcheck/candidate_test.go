package proposalcheck

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/jupiterquote"
	"github.com/Overclock-Validator/mithril-agent/solana"
	"github.com/Overclock-Validator/mithril-agent/txflow"
)

func candidateFixture(t *testing.T) Candidate {
	t.Helper()
	policy, request, proposal := proposalFixture()
	logs := hex.EncodeToString(make([]byte, 32))
	checked, err := check(
		t.Context(), &fakeBuilder{result: proposal}, &fakeEvidence{
			fee: txflow.FeeEvidence{
				Lamports: 5_000, PrimaryContextSlot: 100, SecondaryContextSlot: 100,
			},
			simulations: []txflow.LegacySimulationEvidence{
				{ContextSlot: 100, UnitsConsumed: 40_000, LogsSHA256: logs},
				{ContextSlot: 100, UnitsConsumed: 40_000, LogsSHA256: logs},
			},
		}, primarySlot(100), secondarySlot(100), policy, request,
	)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := checked.Candidate()
	if err != nil {
		t.Fatal(err)
	}
	return candidate
}

func TestCandidateRejectsEveryMaterialMutation(t *testing.T) {
	original := candidateFixture(t)
	message, err := base64.StdEncoding.DecodeString(original.MessageBase64)
	if err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Candidate){
		"version": func(value *Candidate) { value.Version++ },
		"policy":  func(value *Candidate) { value.Policy.MaxInputAmount = 0 },
		"request": func(value *Candidate) { value.Request.InputAmount++ },
		"quote":   func(value *Candidate) { value.Quote.MinimumOutput++ },
		"message": func(value *Candidate) {
			changed := bytes.Clone(message)
			changed[len(changed)-1] ^= 1
			value.MessageBase64 = base64.StdEncoding.EncodeToString(changed)
		},
		"lifetime": func(value *Candidate) { value.LastValidBlockHeight = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := original
			mutate(&changed)
			if _, err := EncodeCandidate(changed); err == nil {
				t.Fatal("materially changed candidate encoded successfully")
			}
		})
	}
}

func TestDecodeCandidateIsStrictAndBounded(t *testing.T) {
	valid, err := EncodeCandidate(candidateFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	unknown := append(bytes.TrimSuffix(bytes.Clone(valid), []byte("}")), []byte(`,"unknown":1}`)...)
	duplicate := []byte(`{"version":1,"version":1}`)
	for name, data := range map[string][]byte{
		"trailing value":  append(bytes.Clone(valid), []byte(` {}`)...),
		"unknown field":   unknown,
		"duplicate field": duplicate,
		"oversized":       bytes.Repeat([]byte(" "), maxCandidateBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeCandidate(data); err == nil {
				t.Fatal("invalid candidate decoded successfully")
			}
		})
	}

	var wire map[string]any
	if err := json.Unmarshal(valid, &wire); err != nil {
		t.Fatal(err)
	}
	wire["message_base64"] = "not base64"
	badMessage, err := json.Marshal(wire)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeCandidate(badMessage); err == nil {
		t.Fatal("invalid candidate message decoded successfully")
	}
}

func TestCandidateRoundTripsAndRechecksAddressLookupTable(t *testing.T) {
	policy, request, proposal := proposalFixture()
	addFallbackLookupAccounts(t, &proposal)
	logs := hex.EncodeToString(make([]byte, 32))
	checked, err := check(
		t.Context(), &fakeBuilder{result: proposal}, &fakeEvidence{
			fee: txflow.FeeEvidence{
				Lamports: 5_000, PrimaryContextSlot: 100, SecondaryContextSlot: 100,
			},
			simulations: []txflow.LegacySimulationEvidence{
				{ContextSlot: 100, UnitsConsumed: 40_000, LogsSHA256: logs},
				{ContextSlot: 100, UnitsConsumed: 40_000, LogsSHA256: logs},
			},
		}, primarySlot(100), secondarySlot(100), policy, request,
	)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := checked.Candidate()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeCandidate(candidate)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err = DecodeCandidate(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidate.AddressTables) != 1 || checked.AddressTableCount != 1 {
		t.Fatalf("address tables = %d, report count = %d", len(candidate.AddressTables), checked.AddressTableCount)
	}
	_, tables, err := ValidateCandidateMaterial(policy, candidate)
	if err != nil {
		t.Fatal(err)
	}
	tables[[32]byte{9}][0][0] ^= 1
	_, detachedTables, err := ValidateCandidateMaterial(policy, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if tables[[32]byte{9}][0] == detachedTables[[32]byte{9}][0] {
		t.Fatal("validated address tables alias candidate material")
	}
	rechecked, err := Recheck(
		t.Context(), &fakeEvidence{
			fee: txflow.FeeEvidence{
				Lamports: 5_000, PrimaryContextSlot: 100, SecondaryContextSlot: 100,
			},
			simulation: txflow.LegacySimulationEvidence{
				ContextSlot: 100, UnitsConsumed: 40_000, LogsSHA256: logs,
			},
		}, primarySlot(100), secondarySlot(100), policy, checkedProviderBindings(), candidate,
	)
	if err != nil {
		t.Fatal(err)
	}
	if rechecked.MessageSHA256 != checked.MessageSHA256 || rechecked.AddressTableCount != 1 {
		t.Fatalf("rechecked result = %+v", rechecked)
	}
}

func addFallbackLookupAccounts(t *testing.T, proposal *jupiterquote.BuildResult) {
	t.Helper()
	route := &proposal.Instructions[3]
	lookupAddress, err := solana.Decode32(route.Accounts[10].Address)
	if err != nil {
		t.Fatal(err)
	}
	table := [][32]byte{lookupAddress}
	for index := byte(0); index < 53; index++ {
		account := [32]byte{0x71, index + 1}
		table = append(table, account)
		route.Accounts = append(route.Accounts, solana.AccountMeta{Address: solana.Encode(account[:])})
	}
	proposal.ClaimedAddressTables = map[[32]byte][][32]byte{{9}: table}
}

func TestValidateCandidateMaterialRequiresProtectedPolicyAndReturnsCopies(t *testing.T) {
	candidate := candidateFixture(t)
	message, tables, err := ValidateCandidateMaterial(candidate.Policy, candidate)
	if err != nil {
		t.Fatal(err)
	}
	message[0] ^= 1
	for key, addresses := range tables {
		addresses[0][0] ^= 1
		tables[key] = addresses
	}
	secondMessage, _, err := ValidateCandidateMaterial(candidate.Policy, candidate)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(message, secondMessage) {
		t.Fatal("validated message aliases candidate material")
	}
	changedPolicy := candidate.Policy
	changedPolicy.MaxInputAmount--
	if _, _, err := ValidateCandidateMaterial(changedPolicy, candidate); err == nil {
		t.Fatal("candidate validated against different protected policy")
	}
}
