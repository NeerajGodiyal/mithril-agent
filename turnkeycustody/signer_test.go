package turnkeycustody

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/signer"
	"github.com/Overclock-Validator/mithril-agent/solana"
	turnkey "github.com/tkhq/go-sdk/v2"
)

type transactionClientFunc func(context.Context, turnkey.SignTransactionRequest) (*turnkey.SignTransactionResponse, error)

type testStamper struct{}

func (testStamper) Stamp(context.Context, []byte) (*turnkey.Stamp, error) {
	return &turnkey.Stamp{HeaderName: "X-Stamp", HeaderValue: "test"}, nil
}

func (function transactionClientFunc) SignTransaction(
	ctx context.Context,
	request turnkey.SignTransactionRequest,
) (*turnkey.SignTransactionResponse, error) {
	return function(ctx, request)
}

func TestSignerSendsOneExactIdempotentSolanaActivity(t *testing.T) {
	request, signed := custodyFixture()
	var captured turnkey.SignTransactionRequest
	client := transactionClientFunc(func(
		_ context.Context,
		input turnkey.SignTransactionRequest,
	) (*turnkey.SignTransactionResponse, error) {
		captured = input
		return completedResponse(input, signed), nil
	})
	custody, err := newSigner(client, Config{OrganizationID: "organization", SignWith: "wallet"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := custody.Sign(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, signed) || captured.OrganizationID != "organization" ||
		captured.TimestampMs != "1700000000123" || captured.SignWith != "wallet" ||
		captured.TypeValue != turnkey.TransactionTypeSolana ||
		captured.UnsignedTransaction != hex.EncodeToString(request.Transaction) {
		t.Fatalf("Turnkey activity was not exact: %+v", captured)
	}
}

func TestOfficialSDKReusesTheExactTurnkeyActivityBody(t *testing.T) {
	request, signed := custodyFixture()
	var bodies [][]byte
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, httpRequest *http.Request) {
		if httpRequest.URL.Path != "/public/v1/submit/sign_transaction" {
			t.Fatalf("Turnkey path = %q", httpRequest.URL.Path)
		}
		body, err := io.ReadAll(httpRequest.Body)
		if err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, body)
		var activity struct {
			Type           turnkey.ActivityType `json:"type"`
			TimestampMs    string               `json:"timestampMs"`
			OrganizationID string               `json:"organizationId"`
			Parameters     struct {
				SignWith            string                  `json:"signWith"`
				UnsignedTransaction string                  `json:"unsignedTransaction"`
				TypeValue           turnkey.TransactionType `json:"type"`
			} `json:"parameters"`
		}
		if err := json.Unmarshal(body, &activity); err != nil {
			t.Fatal(err)
		}
		if len(bodies) == 1 {
			http.Error(writer, "temporary failure", http.StatusServiceUnavailable)
			return
		}
		response := map[string]any{"activity": map[string]any{
			"id": "activity", "organizationId": activity.OrganizationID,
			"status": turnkey.ActivityStatusCompleted, "type": activity.Type,
			"intent": map[string]any{"signTransactionIntentV2": map[string]any{
				"signWith":            activity.Parameters.SignWith,
				"unsignedTransaction": activity.Parameters.UnsignedTransaction,
				"type":                activity.Parameters.TypeValue,
			}},
			"result": map[string]any{"signTransactionResult": map[string]any{
				"signedTransaction": hex.EncodeToString(signed),
			}},
		}}
		writer.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(writer).Encode(response); err != nil {
			t.Fatal(err)
		}
	}))
	defer server.Close()

	client, err := turnkey.NewClient(
		testStamper{}, "organization", turnkey.WithBaseURL(server.URL),
		turnkey.WithHTTPClient(server.Client()), turnkey.WithHTTPRetries(1),
		turnkey.WithHTTPRetryDelay(time.Millisecond),
	)
	if err != nil {
		t.Fatal(err)
	}
	custody, err := newSigner(client, Config{OrganizationID: "organization", SignWith: "wallet"})
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := custody.Sign(context.Background(), request); err != nil {
			t.Fatal(err)
		}
	}
	if len(bodies) != 3 || !bytes.Equal(bodies[0], bodies[1]) ||
		!bytes.Equal(bodies[1], bodies[2]) ||
		!bytes.Contains(bodies[0], []byte(`"timestampMs":"1700000000123"`)) {
		t.Fatalf("Turnkey retries did not reuse the exact activity body: %q", bodies)
	}
}

func TestSignerRejectsProviderResponseDrift(t *testing.T) {
	request, signed := custodyFixture()
	for name, mutate := range map[string]func(*turnkey.SignTransactionResponse){
		"organization": func(response *turnkey.SignTransactionResponse) {
			response.Activity.OrganizationID = "other"
		},
		"activity type": func(response *turnkey.SignTransactionResponse) {
			response.Activity.TypeValue = turnkey.ActivityTypeSignRawPayloadV2
		},
		"status": func(response *turnkey.SignTransactionResponse) {
			response.Activity.Status = turnkey.ActivityStatusPending
		},
		"signer": func(response *turnkey.SignTransactionResponse) {
			response.Activity.Intent.SignTransactionIntentV2.SignWith = "other"
		},
		"transaction type": func(response *turnkey.SignTransactionResponse) {
			response.Activity.Intent.SignTransactionIntentV2.TypeValue = turnkey.TransactionTypeEthereum
		},
		"unsigned transaction": func(response *turnkey.SignTransactionResponse) {
			response.Activity.Intent.SignTransactionIntentV2.UnsignedTransaction = "00"
		},
		"malformed signed transaction": func(response *turnkey.SignTransactionResponse) {
			response.SignedTransaction = "not-hex"
		},
		"different signed transaction size": func(response *turnkey.SignTransactionResponse) {
			response.SignedTransaction += "00"
		},
		"invalid Solana signature": func(response *turnkey.SignTransactionResponse) {
			response.SignedTransaction = hex.EncodeToString(append([]byte(nil), signed...))
			response.SignedTransaction = response.SignedTransaction[:2] + "00" + response.SignedTransaction[4:]
		},
		"different signed message": func(response *turnkey.SignTransactionResponse) {
			changed := append([]byte(nil), signed...)
			changed[len(changed)-1] ^= 1
			response.SignedTransaction = hex.EncodeToString(changed)
		},
	} {
		t.Run(name, func(t *testing.T) {
			client := transactionClientFunc(func(
				_ context.Context,
				input turnkey.SignTransactionRequest,
			) (*turnkey.SignTransactionResponse, error) {
				response := completedResponse(input, signed)
				mutate(response)
				return response, nil
			})
			custody, err := newSigner(client, Config{OrganizationID: "organization", SignWith: "wallet"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := custody.Sign(context.Background(), request); err == nil {
				t.Fatal("drifted Turnkey response was accepted")
			}
		})
	}
}

func TestSignerFailsClosedWithoutLeakingProviderErrors(t *testing.T) {
	request, _ := custodyFixture()
	const privateProviderError = "private provider response body"
	client := transactionClientFunc(func(
		context.Context,
		turnkey.SignTransactionRequest,
	) (*turnkey.SignTransactionResponse, error) {
		return nil, errors.New(privateProviderError)
	})
	custody, err := newSigner(client, Config{OrganizationID: "organization", SignWith: "wallet"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = custody.Sign(context.Background(), request)
	if err == nil || IsSigningRefused(err) || strings.Contains(err.Error(), privateProviderError) {
		t.Fatalf("provider error was not bounded: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := custody.Sign(ctx, request); err == nil || !strings.Contains(err.Error(), "canceled") {
		t.Fatalf("canceled request = %v", err)
	}
}

func TestSignerExplainsApprovalRequirementWithoutLeakingActivity(t *testing.T) {
	request, _ := custodyFixture()
	const privateActivityID = "private-consensus-activity"
	client := transactionClientFunc(func(
		context.Context,
		turnkey.SignTransactionRequest,
	) (*turnkey.SignTransactionResponse, error) {
		return nil, &turnkey.ActivityRequiresApprovalError{ActivityID: privateActivityID}
	})
	custody, err := newSigner(client, Config{OrganizationID: "organization", SignWith: "wallet"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = custody.Sign(t.Context(), request)
	if err == nil || IsSigningRefused(err) ||
		!strings.Contains(err.Error(), "requires approval") ||
		strings.Contains(err.Error(), privateActivityID) {
		t.Fatalf("Turnkey approval result = %v", err)
	}
}

func TestSignerClassifiesProviderAvailabilityWithoutLeakingResponse(t *testing.T) {
	request, _ := custodyFixture()
	for _, test := range []struct {
		name, want string
		status     int
	}{
		{name: "rate or plan limit", status: http.StatusTooManyRequests, want: "rate or plan limited"},
		{name: "provider outage", status: http.StatusServiceUnavailable, want: "temporarily unavailable"},
	} {
		t.Run(test.name, func(t *testing.T) {
			const privateProviderBody = "private provider response"
			client := transactionClientFunc(func(
				context.Context,
				turnkey.SignTransactionRequest,
			) (*turnkey.SignTransactionResponse, error) {
				return nil, &turnkey.RequestError{
					StatusCode: test.status,
					Body:       []byte(privateProviderBody),
				}
			})
			custody, err := newSigner(client, Config{OrganizationID: "organization", SignWith: "wallet"})
			if err != nil {
				t.Fatal(err)
			}
			_, err = custody.Sign(t.Context(), request)
			if err == nil || IsSigningRefused(err) ||
				!strings.Contains(err.Error(), test.want) ||
				strings.Contains(err.Error(), privateProviderBody) {
				t.Fatalf("Turnkey provider availability result = %v", err)
			}
		})
	}
}

func TestSignerClassifiesProviderRefusalWithoutLeakingFailure(t *testing.T) {
	request, _ := custodyFixture()
	const privateProviderFailure = "private policy evaluation detail"
	client := transactionClientFunc(func(
		context.Context,
		turnkey.SignTransactionRequest,
	) (*turnkey.SignTransactionResponse, error) {
		return nil, &turnkey.ActivityFailedError{
			ActivityID: "rejected-activity",
			Status:     turnkey.ActivityStatusRejected,
			Failure:    &turnkey.RPCStatus{Message: stringPointer(privateProviderFailure)},
		}
	})
	custody, err := newSigner(client, Config{OrganizationID: "organization", SignWith: "wallet"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = custody.Sign(t.Context(), request)
	if !IsSigningRefused(err) || strings.Contains(err.Error(), privateProviderFailure) {
		t.Fatalf("Turnkey policy refusal = %v", err)
	}
}

func TestSignerDoesNotMistakeFailedActivityForPolicyRefusal(t *testing.T) {
	request, _ := custodyFixture()
	const privateProviderFailure = "private activity failure detail"
	client := transactionClientFunc(func(
		context.Context,
		turnkey.SignTransactionRequest,
	) (*turnkey.SignTransactionResponse, error) {
		return nil, &turnkey.ActivityFailedError{
			ActivityID: "failed-activity",
			Status:     turnkey.ActivityStatusFailed,
			Failure:    &turnkey.RPCStatus{Message: stringPointer(privateProviderFailure)},
		}
	})
	custody, err := newSigner(client, Config{OrganizationID: "organization", SignWith: "wallet"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = custody.Sign(t.Context(), request)
	if err == nil || IsSigningRefused(err) || strings.Contains(err.Error(), privateProviderFailure) {
		t.Fatalf("failed Turnkey activity was treated as a policy refusal: %v", err)
	}
}

func stringPointer(value string) *string { return &value }

func TestSignerHonorsCallerDeadline(t *testing.T) {
	request, _ := custodyFixture()
	client := transactionClientFunc(func(
		ctx context.Context,
		_ turnkey.SignTransactionRequest,
	) (*turnkey.SignTransactionResponse, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	})
	custody, err := newSigner(client, Config{OrganizationID: "organization", SignWith: "wallet"})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := custody.Sign(ctx, request); err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("deadline result = %v", err)
	}
}

func TestSignerRejectsInvalidBoundaryInputs(t *testing.T) {
	request, _ := custodyFixture()
	client := transactionClientFunc(func(
		context.Context,
		turnkey.SignTransactionRequest,
	) (*turnkey.SignTransactionResponse, error) {
		t.Fatal("invalid request reached Turnkey")
		return nil, nil
	})
	for name, mutate := range map[string]func(*signer.TransactionCustodyRequest){
		"request digest": func(value *signer.TransactionCustodyRequest) { value.RequestSHA256 = "A" + value.RequestSHA256[1:] },
		"timestamp":      func(value *signer.TransactionCustodyRequest) { value.TimestampMS = 0 },
		"signature count": func(value *signer.TransactionCustodyRequest) {
			value.Transaction[0] = 2
		},
		"pre-signed": func(value *signer.TransactionCustodyRequest) { value.Transaction[1] = 1 },
		"legacy":     func(value *signer.TransactionCustodyRequest) { value.Transaction[65] = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			changed := request
			changed.Transaction = append([]byte(nil), request.Transaction...)
			mutate(&changed)
			custody, err := newSigner(client, Config{OrganizationID: "organization", SignWith: "wallet"})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := custody.Sign(context.Background(), changed); err == nil {
				t.Fatal("invalid custody request was accepted")
			}
		})
	}

	if _, err := newSigner(client, Config{OrganizationID: " organization", SignWith: "wallet"}); err == nil {
		t.Fatal("invalid custody configuration was accepted")
	}
	if _, err := newWithStamper(nil, Config{OrganizationID: "organization", SignWith: "wallet"}); err == nil {
		t.Fatal("missing Turnkey stamper was accepted")
	}
}

func TestAPIKeyFileAcceptsTurnkeyCLIFormat(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "api.private")
	if err := os.WriteFile(path, []byte(strings.Repeat("1", 64)+":p256\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stamper, err := loadAPIKeyStamper(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadVerifiedAPIKeyStamper(path, stamper.PublicKey()); err != nil {
		t.Fatal(err)
	}
	if _, err := loadVerifiedAPIKeyStamper(path, "different"); err == nil {
		t.Fatal("mismatched Turnkey API key pair was accepted")
	} else if !strings.Contains(err.Error(), ".private and .public") {
		t.Fatalf("mismatched Turnkey key guidance = %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadVerifiedAPIKeyStamper(path, stamper.PublicKey()); err == nil {
		t.Fatal("permissive Turnkey API private-key file was accepted")
	} else if !strings.Contains(err.Error(), "mode-0600 .private") {
		t.Fatalf("unsafe Turnkey key guidance = %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadVerifiedAPIKeyStamper(path, stamper.PublicKey()); err == nil {
		t.Fatal("Turnkey activity-like JSON was accepted as a private key")
	} else if !strings.Contains(err.Error(), "not a dashboard activity JSON") {
		t.Fatalf("invalid Turnkey key guidance = %v", err)
	}
}

func TestSecureHTTPClientBoundsResponsesAndRefusesRedirects(t *testing.T) {
	var redirected atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirected.Add(1)
	}))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusFound)
	}))
	defer redirect.Close()

	client := secureHTTPClient(redirect.URL)
	bounded, ok := client.Transport.(*boundedTransport)
	if !ok {
		t.Fatal("Turnkey transport is not origin-bounded")
	}
	base, ok := bounded.base.(*http.Transport)
	if !ok || base.Proxy != nil || base.TLSClientConfig == nil ||
		base.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Fatal("Turnkey transport inherited an environment proxy")
	}
	response, err := client.Get(redirect.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusFound || redirected.Load() != 0 {
		t.Fatalf("redirect status = %d, followed = %d", response.StatusCode, redirected.Load())
	}
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, target.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Transport.RoundTrip(request); err == nil {
		t.Fatal("Turnkey transport accepted a different origin")
	}

	large := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write(bytes.Repeat([]byte{'x'}, maxResponseBytes+1024))
	}))
	defer large.Close()
	client = secureHTTPClient(large.URL)
	response, err = client.Get(large.URL)
	if err == nil {
		_, err = io.ReadAll(response.Body)
		_ = response.Body.Close()
	}
	if !errors.Is(err, errResponseTooLarge) {
		t.Fatalf("oversized response error = %v", err)
	}
}

func custodyFixture() (signer.TransactionCustodyRequest, []byte) {
	seed := bytes.Repeat([]byte{7}, ed25519.SeedSize)
	privateKey := ed25519.NewKeyFromSeed(seed)
	publicKey := privateKey.Public().(ed25519.PublicKey)
	message := make([]byte, 0, 70)
	message = append(message, 0x80, 1, 0, 0, 1)
	message = append(message, publicKey...)
	message = append(message, make([]byte, 32)...)
	message = append(message, 0, 0)
	unsigned := append([]byte{1}, make([]byte, ed25519.SignatureSize)...)
	unsigned = append(unsigned, message...)
	signed := append([]byte{1}, ed25519.Sign(privateKey, message)...)
	signed = append(signed, message...)
	if _, err := solana.VerifySignedTransactionEnvelope(signed); err != nil {
		panic(err)
	}
	return signer.TransactionCustodyRequest{
		RequestSHA256: strings.Repeat("a", 64), TimestampMS: 1_700_000_000_123,
		Transaction: unsigned,
	}, signed
}

func completedResponse(
	request turnkey.SignTransactionRequest,
	signed []byte,
) *turnkey.SignTransactionResponse {
	return &turnkey.SignTransactionResponse{
		Activity: turnkey.Activity{
			OrganizationID: request.OrganizationID,
			Status:         turnkey.ActivityStatusCompleted,
			TypeValue:      turnkey.ActivityTypeSignTransactionV2,
			Intent: turnkey.Intent{SignTransactionIntentV2: &turnkey.SignTransactionIntentV2{
				SignWith: request.SignWith, TypeValue: request.TypeValue,
				UnsignedTransaction: request.UnsignedTransaction,
			}},
		},
		SignTransactionResult: turnkey.SignTransactionResult{SignedTransaction: hex.EncodeToString(signed)},
	}
}
