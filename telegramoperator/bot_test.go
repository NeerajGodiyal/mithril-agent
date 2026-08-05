package telegramoperator

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const testBotToken = "123456789:ABCDEFGHIJKLMNOPQRSTUVWXYZ_abcd"

func TestHTTPBotPollUsesBoundedMessageUpdates(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost ||
			request.URL.Path != "/bot"+testBotToken+"/getUpdates" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		var body struct {
			Offset         int64    `json:"offset"`
			Limit          int      `json:"limit"`
			Timeout        int64    `json:"timeout"`
			AllowedUpdates []string `json:"allowed_updates"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Offset != 41 || body.Limit != 100 || body.Timeout != 25 ||
			len(body.AllowedUpdates) != 1 || body.AllowedUpdates[0] != "message" {
			t.Fatalf("body = %+v", body)
		}
		writer.Header().Set("Content-Type", "application/json")
		_, _ = writer.Write([]byte(`{"ok":true,"result":[{"update_id":41,"message":{"text":"/status","chat":{"id":77}}}]}`))
	}))
	t.Cleanup(server.Close)
	bot, err := newHTTPBot(testBotToken, server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	updates, err := bot.Poll(t.Context(), 41, 25*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 || updates[0] != (Update{ID: 41, ChatID: 77, Text: "/status"}) {
		t.Fatalf("updates = %+v", updates)
	}
}

func TestHTTPBotSendRequiresPositiveAcknowledgement(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/bot"+testBotToken+"/sendMessage" {
			t.Fatalf("path = %q", request.URL.Path)
		}
		var body struct {
			ChatID             int64  `json:"chat_id"`
			Text               string `json:"text"`
			LinkPreviewOptions struct {
				Disabled bool `json:"is_disabled"`
			} `json:"link_preview_options"`
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.ChatID != 77 || body.Text != "healthy" || !body.LinkPreviewOptions.Disabled {
			t.Fatalf("body = %+v", body)
		}
		_, _ = writer.Write([]byte(`{"ok":true,"result":{"message_id":1}}`))
	}))
	t.Cleanup(server.Close)
	bot, err := newHTTPBot(testBotToken, server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := bot.Send(t.Context(), 77, "healthy"); err != nil {
		t.Fatal(err)
	}
}

func TestHTTPBotSuppressesTokenAndProviderBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"ok":false,"description":"private provider detail"}`))
	}))
	t.Cleanup(server.Close)
	bot, err := newHTTPBot(testBotToken, server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	err = bot.Send(t.Context(), 77, "healthy")
	if err == nil || strings.Contains(err.Error(), testBotToken) ||
		strings.Contains(err.Error(), "private provider detail") {
		t.Fatalf("error = %v", err)
	}
}

func TestHTTPBotRejectsMissingSendAcknowledgement(t *testing.T) {
	for _, body := range []string{
		`{"ok":true}`,
		`{"ok":true,"result":null}`,
		`{"ok":true,"result":{}}`,
	} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(body))
			}))
			t.Cleanup(server.Close)
			bot, err := newHTTPBot(testBotToken, server.URL, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if err := bot.Send(t.Context(), 77, "healthy"); err == nil {
				t.Fatal("missing Telegram send acknowledgement was accepted")
			}
		})
	}
}

func TestHTTPBotRejectsTrailingResponseData(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"ok":true,"result":[]} {"extra":true}`))
	}))
	t.Cleanup(server.Close)
	bot, err := newHTTPBot(testBotToken, server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bot.Poll(t.Context(), 0, time.Second); err == nil {
		t.Fatal("trailing Telegram response data was accepted")
	}
}

func TestHTTPBotRejectsRedirectAndOversizedResponse(t *testing.T) {
	redirected := false
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/redirected" {
			redirected = true
			_, _ = writer.Write([]byte(`{"ok":true,"result":[]}`))
			return
		}
		http.Redirect(writer, request, "/redirected", http.StatusFound)
	}))
	t.Cleanup(server.Close)
	bot, err := newHTTPBot(testBotToken, server.URL, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bot.Poll(t.Context(), 0, time.Second); err == nil || redirected {
		t.Fatalf("error=%v redirected=%v", err, redirected)
	}

	large := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"ok":true,"result":"`))
		_, _ = writer.Write([]byte(strings.Repeat("x", maxTelegramBodyBytes)))
	}))
	t.Cleanup(large.Close)
	bot, err = newHTTPBot(testBotToken, large.URL, large.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bot.Poll(t.Context(), 0, time.Second); err == nil {
		t.Fatal("oversized Telegram response was accepted")
	}
}

func TestHTTPBotRejectsUnsafeInputs(t *testing.T) {
	for _, token := range []string{"", "123:short", "123456789:bad/secret________________"} {
		if _, err := NewHTTPBot(token, nil); err == nil {
			t.Fatalf("token %q accepted", token)
		}
	}
	if _, err := newHTTPBot(testBotToken, "http://example.com", nil); err == nil {
		t.Fatal("non-loopback HTTP origin accepted")
	}
	bot, err := newHTTPBot(testBotToken, "http://127.0.0.1:1", &http.Client{
		Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, errors.New("secret transport detail")
		}),
	})
	if err != nil {
		t.Fatal(err)
	}
	err = bot.Send(context.Background(), 77, "healthy")
	if err == nil || strings.Contains(err.Error(), "secret transport detail") {
		t.Fatalf("error = %v", err)
	}
}

func TestHTTPBotAlwaysHasABoundedClientTimeout(t *testing.T) {
	bot, err := NewHTTPBot(testBotToken, &http.Client{})
	if err != nil {
		t.Fatal(err)
	}
	if bot.client.Timeout != maxTelegramHTTPTime {
		t.Fatalf("default timeout = %s", bot.client.Timeout)
	}
	bot, err = NewHTTPBot(testBotToken, &http.Client{Timeout: 2 * maxTelegramHTTPTime})
	if err != nil {
		t.Fatal(err)
	}
	if bot.client.Timeout != maxTelegramHTTPTime {
		t.Fatalf("capped timeout = %s", bot.client.Timeout)
	}
	bot, err = NewHTTPBot(testBotToken, &http.Client{Timeout: 5 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if bot.client.Timeout != 5*time.Second {
		t.Fatalf("short timeout = %s", bot.client.Timeout)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}
