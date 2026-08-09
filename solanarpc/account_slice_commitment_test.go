package solanarpc

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Overclock-Validator/mithril-agent/solana"
)

// A Mithril node replays and can describe only its own processed state; it
// refuses any other commitment outright. AccountSlice is how the on-chain price
// feed is read, so asking that node for "confirmed" makes the agent's own
// price rule permanently unreadable while the node is perfectly healthy.
func TestAccountSliceUsesProcessedForMithrilNode(t *testing.T) {
	for _, test := range []struct {
		name    string
		mithril bool
		want    string
	}{
		{name: "mithril node", mithril: true, want: `"commitment":"processed"`},
		{name: "evidence provider", mithril: false, want: `"commitment":"confirmed"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			seen := false
			httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				var input struct {
					ID     uint64          `json:"id"`
					Method string          `json:"method"`
					Params json.RawMessage `json:"params"`
				}
				if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
					t.Fatal(err)
				}
				if input.Method != "getAccountInfo" {
					t.Fatalf("unexpected method %q", input.Method)
				}
				seen = true
				assertContains(t, input.Params, test.want)
				return jsonResponse(t, map[string]any{
					"jsonrpc": "2.0", "id": input.ID,
					"result": map[string]any{
						"context": map[string]any{"slot": uint64(90)},
						"value": map[string]any{
							"owner": solana.Encode(bytes.Repeat([]byte{3}, 32)),
							"data": []any{
								base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 134)),
								"base64",
							},
							"space": uint64(134),
						},
					},
				}), nil
			})}

			var client *Client
			var err error
			if test.mithril {
				client, err = NewMithrilNode("http://127.0.0.1:8899", httpClient)
			} else {
				client, err = New("https://example.invalid", httpClient, false)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.AccountSlice(
				t.Context(), solana.Encode(bytes.Repeat([]byte{9}, 32)), 80, 0, 134,
			); err != nil {
				t.Fatalf("AccountSlice: %v", err)
			}
			if !seen {
				t.Fatal("no getAccountInfo request was made")
			}
		})
	}
}
