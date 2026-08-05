package openaiexplainer

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/telegramoperator"
)

const testKey = "test-api-key"

func TestExplainSendsBoundedResponsesRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodPost || request.URL.Path != "/v1/responses" ||
			request.URL.RawQuery != "" || request.Header.Get("Authorization") != "Bearer "+testKey ||
			request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request metadata")
		}
		var body requestBody
		decoder := json.NewDecoder(request.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body.Model != "gpt-test" || body.Instructions != instructions || body.Store ||
			body.MaxOutputTokens != maxOutputTokens || body.Tools == nil || len(body.Tools) != 0 ||
			len(body.Input) != 1 || body.Input[0].Role != "user" || len(body.Input[0].Content) != 1 ||
			body.Input[0].Content[0].Type != "input_text" ||
			!strings.Contains(body.Input[0].Content[0].Text, "Question:\nWhy stopped?") ||
			!strings.Contains(body.Input[0].Content[0].Text, "Status:\nStatus: stopped") {
			t.Errorf("unexpected request body: %+v", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{
			"status":"completed",
			"output":[
				{"type":"reasoning","content":[]},
				{"type":"message","status":"completed","role":"assistant","content":[
					{"type":"output_text","text":"The runner is stopped."},
					{"type":"refusal","text":"ignored"},
					{"type":"output_text","text":"Verify deterministic status."}
				]}
			]
		}`))
	}))
	defer server.Close()

	client, err := NewWithBaseURL(testKey, "gpt-test", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	text, err := client.Explain(ctx, validRequest())
	if err != nil {
		t.Fatal(err)
	}
	if text != "The runner is stopped.\nVerify deterministic status." || calls.Load() != 1 {
		t.Fatalf("text=%q calls=%d", text, calls.Load())
	}
}

func TestExplainRequiresCompletedOutputText(t *testing.T) {
	tests := map[string]string{
		"incomplete response": `{"status":"in_progress","output":[{"type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":"not final"}]}]}`,
		"incomplete message":  `{"status":"completed","output":[{"type":"message","status":"in_progress","role":"assistant","content":[{"type":"output_text","text":"not final"}]}]}`,
		"refusal":             `{"status":"completed","output":[{"type":"message","status":"completed","role":"assistant","content":[{"type":"refusal","text":"not output text"}]}]}`,
		"wrong role":          `{"status":"completed","output":[{"type":"message","status":"completed","role":"user","content":[{"type":"output_text","text":"not assistant"}]}]}`,
	}
	for name, response := range tests {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(response))
			}))
			defer server.Close()
			client, err := NewWithBaseURL(testKey, "gpt-test", server.URL)
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			if text, err := client.Explain(ctx, validRequest()); err == nil || text != "" {
				t.Fatalf("text=%q err=%v", text, err)
			}
		})
	}
}

func TestExplainNeverRetriesOrExposesProviderBody(t *testing.T) {
	const privateBody = "provider-secret-debug-body"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		http.Error(writer, privateBody, http.StatusTooManyRequests)
	}))
	defer server.Close()
	client, err := NewWithBaseURL(testKey, "gpt-test", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	_, err = client.Explain(ctx, validRequest())
	if err == nil || strings.Contains(err.Error(), privateBody) ||
		strings.Contains(err.Error(), testKey) || calls.Load() != 1 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}
}

func TestExplainCapsResponseAndRejectsRedirects(t *testing.T) {
	t.Run("body cap", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			_, _ = writer.Write([]byte(strings.Repeat("x", maxResponseBodyBytes+1)))
		}))
		defer server.Close()
		client, err := NewWithBaseURL(testKey, "gpt-test", server.URL)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if _, err := client.Explain(ctx, validRequest()); err == nil {
			t.Fatal("oversized response was accepted")
		}
	})

	t.Run("redirect", func(t *testing.T) {
		var redirected atomic.Int32
		target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			redirected.Add(1)
		}))
		defer target.Close()
		server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			http.Redirect(writer, &http.Request{}, target.URL, http.StatusTemporaryRedirect)
		}))
		defer server.Close()
		client, err := NewWithBaseURL(testKey, "gpt-test", server.URL)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if _, err := client.Explain(ctx, validRequest()); err == nil || redirected.Load() != 0 {
			t.Fatalf("redirect err=%v followed=%d", err, redirected.Load())
		}
	})
}

func TestExplainUsesCallerDeadline(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		select {
		case <-request.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	client, err := NewWithBaseURL(testKey, "gpt-test", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Explain(context.Background(), validRequest()); err == nil || calls.Load() != 0 {
		t.Fatalf("missing deadline err=%v calls=%d", err, calls.Load())
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := client.Explain(ctx, validRequest()); err == nil || calls.Load() != 1 ||
		time.Since(started) > time.Second {
		t.Fatalf("deadline err=%v calls=%d elapsed=%s", err, calls.Load(), time.Since(started))
	}
}

func TestConstructorsRestrictOriginsAndCredentials(t *testing.T) {
	client, err := New(testKey, "gpt-test")
	if err != nil || client.endpoint != officialOrigin+"/v1/responses" {
		t.Fatalf("official client=%+v err=%v", client, err)
	}
	for _, accepted := range []string{"http://127.0.0.1:8080", "https://[::1]:8443/"} {
		if _, err := NewWithBaseURL(testKey, "gpt-test", accepted); err != nil {
			t.Fatalf("rejected loopback origin %q: %v", accepted, err)
		}
	}
	for _, rejected := range []string{
		"http://api.openai.com", "https://example.com", "http://localhost:8080",
		"http://127.0.0.1:8080/path", "http://127.0.0.1:8080?key=value",
		"http://127.0.0.1:8080#fragment", "http://user:pass@127.0.0.1:8080",
	} {
		if _, err := NewWithBaseURL(testKey, "gpt-test", rejected); err == nil {
			t.Fatalf("accepted invalid origin %q", rejected)
		}
	}
	for _, values := range [][2]string{{"", "gpt-test"}, {" key", "gpt-test"}, {testKey, ""}, {testKey, "bad model"}} {
		if _, err := New(values[0], values[1]); err == nil {
			t.Fatalf("accepted invalid constructor values")
		}
	}
}

func TestExplainRejectsInvalidInputBeforeRequest(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		calls.Add(1)
	}))
	defer server.Close()
	client, err := NewWithBaseURL(testKey, "gpt-test", server.URL)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	requests := []telegramoperator.ExplanationRequest{
		{},
		{Question: " question", StatusText: "status"},
		{Question: strings.Repeat("q", maxQuestionBytes+1), StatusText: "status"},
		{Question: "question", StatusText: strings.Repeat("s", maxStatusBytes+1)},
	}
	for _, request := range requests {
		if _, err := client.Explain(ctx, request); err == nil {
			t.Fatalf("accepted invalid request")
		}
	}
	if calls.Load() != 0 {
		t.Fatalf("invalid requests reached provider: %d", calls.Load())
	}
}

func validRequest() telegramoperator.ExplanationRequest {
	return telegramoperator.ExplanationRequest{
		Question: "Why stopped?", StatusText: "Status: stopped",
		ObservedAt: time.Date(2026, time.August, 2, 12, 0, 0, 0, time.UTC),
	}
}
