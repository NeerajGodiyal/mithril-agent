package solanarpc

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"testing"
)

func TestPublicMithrilProvenanceContractFixture(t *testing.T) {
	path := os.Getenv("MITHRIL_PROVENANCE_CONTRACT_FIXTURE")
	if path == "" {
		t.Skip("set MITHRIL_PROVENANCE_CONTRACT_FIXTURE to the public Mithril fixture")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		GenesisHash        string          `json:"genesis_hash"`
		VerificationStatus json.RawMessage `json:"verification_status"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fixture); err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(fixture.VerificationStatus, &fields); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"state", "required", "verifiedSlot", "eligibleSlot", "healthy", "evidenceServed"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("verification status fixture is missing exact wire key %q", key)
		}
		delete(fields, key)
	}
	if len(fields) != 0 {
		t.Fatalf("verification status fixture has unexpected wire keys: %v", fields)
	}
	client, err := NewMithrilNode("http://127.0.0.1:8899", &http.Client{
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			var input struct {
				ID     uint64 `json:"id"`
				Method string `json:"method"`
			}
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				t.Fatal(err)
			}
			var result json.RawMessage
			switch input.Method {
			case "getGenesisHash":
				encoded, encodeErr := json.Marshal(fixture.GenesisHash)
				if encodeErr != nil {
					t.Fatal(encodeErr)
				}
				result = encoded
			case "getVerificationStatus":
				result = fixture.VerificationStatus
			default:
				t.Fatalf("unexpected method %q", input.Method)
			}
			body, err := json.Marshal(struct {
				JSONRPC string          `json:"jsonrpc"`
				ID      uint64          `json:"id"`
				Result  json.RawMessage `json:"result"`
			}{JSONRPC: "2.0", ID: input.ID, Result: result})
			if err != nil {
				t.Fatal(err)
			}
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(body))}, nil
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	genesis, err := client.GenesisHash(t.Context())
	if err != nil || genesis != fixture.GenesisHash {
		t.Fatalf("genesis=%q err=%v", genesis, err)
	}
	status, err := client.VerificationStatus(t.Context())
	want := VerificationStatus{
		State: "complete", Required: true, VerifiedSlot: 91, EligibleSlot: 91,
		Healthy: true, EvidenceServed: true,
	}
	if err != nil || status != want {
		t.Fatalf("status=%+v want=%+v err=%v", status, want, err)
	}
}
