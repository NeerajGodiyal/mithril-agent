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
		GenesisHash        string             `json:"genesis_hash"`
		VerificationStatus VerificationStatus `json:"verification_status"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fixture); err != nil {
		t.Fatal(err)
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
			var result any
			switch input.Method {
			case "getGenesisHash":
				result = fixture.GenesisHash
			case "getVerificationStatus":
				result = fixture.VerificationStatus
			default:
				t.Fatalf("unexpected method %q", input.Method)
			}
			body, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": input.ID, "result": result})
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
	if err != nil || status != fixture.VerificationStatus {
		t.Fatalf("status=%+v want=%+v err=%v", status, fixture.VerificationStatus, err)
	}
}
