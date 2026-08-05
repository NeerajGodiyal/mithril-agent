package pricesource

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

// liveAccountReader is a minimal JSON-RPC account reader used only by the
// env-gated live smoke test. Production wiring supplies a real Mithril client.
type liveAccountReader struct{ endpoint string }

func (r liveAccountReader) AccountSlice(
	ctx context.Context, address string, _, _, _ uint64,
) (AccountData, error) {
	payload := `{"jsonrpc":"2.0","id":1,"method":"getAccountInfo","params":["` +
		address + `",{"encoding":"base64"}]}`
	request, err := http.NewRequestWithContext(
		ctx, http.MethodPost, r.endpoint, strings.NewReader(payload))
	if err != nil {
		return AccountData{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return AccountData{}, err
	}
	defer response.Body.Close()

	var decoded struct {
		Result struct {
			Context struct {
				Slot uint64 `json:"slot"`
			} `json:"context"`
			Value *struct {
				Data  []string `json:"data"`
				Owner string   `json:"owner"`
			} `json:"value"`
		} `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&decoded); err != nil {
		return AccountData{}, err
	}
	if decoded.Result.Value == nil || len(decoded.Result.Value.Data) == 0 {
		return AccountData{}, errors.New("account absent")
	}
	raw, err := base64.StdEncoding.DecodeString(decoded.Result.Value.Data[0])
	if err != nil {
		return AccountData{}, err
	}
	return AccountData{
		ContextSlot: decoded.Result.Context.Slot,
		Owner:       decoded.Result.Value.Owner,
		DataLength:  uint64(len(raw)),
		Data:        raw,
	}, nil
}
