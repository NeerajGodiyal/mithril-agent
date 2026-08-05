package telegramoperator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	telegramOrigin       = "https://api.telegram.org"
	maxTelegramBodyBytes = 256 << 10
	maxTelegramUpdates   = 100
	maxTelegramHTTPTime  = 60 * time.Second
)

type HTTPBot struct {
	client  *http.Client
	baseURL string
	token   string
}

func NewHTTPBot(token string, client *http.Client) (*HTTPBot, error) {
	return newHTTPBot(token, telegramOrigin, client)
}

func newHTTPBot(token, baseURL string, client *http.Client) (*HTTPBot, error) {
	if !validBotToken(token) {
		return nil, errors.New("Telegram bot token is invalid")
	}
	origin, err := url.Parse(baseURL)
	if err != nil || origin.Scheme == "" || origin.Host == "" || origin.User != nil ||
		origin.RawQuery != "" || origin.Fragment != "" || origin.Path != "" {
		return nil, errors.New("Telegram API origin is invalid")
	}
	if baseURL == telegramOrigin {
		if origin.Scheme != "https" || origin.Hostname() != "api.telegram.org" || origin.Port() != "" {
			return nil, errors.New("Telegram API origin is invalid")
		}
	} else if origin.Scheme != "http" ||
		(origin.Hostname() != "127.0.0.1" && origin.Hostname() != "::1") {
		return nil, errors.New("test Telegram API origin must use literal loopback HTTP")
	}
	if client == nil {
		client = &http.Client{}
	}
	copyClient := *client
	if copyClient.Timeout <= 0 || copyClient.Timeout > maxTelegramHTTPTime {
		copyClient.Timeout = maxTelegramHTTPTime
	}
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &HTTPBot{client: &copyClient, baseURL: baseURL, token: token}, nil
}

func (b *HTTPBot) Poll(ctx context.Context, offset int64, timeout time.Duration) ([]Update, error) {
	seconds := int64(timeout / time.Second)
	if offset < 0 || seconds < 1 || seconds > 50 {
		return nil, errors.New("Telegram poll parameters are invalid")
	}
	request := struct {
		Offset         int64    `json:"offset"`
		Limit          int      `json:"limit"`
		Timeout        int64    `json:"timeout"`
		AllowedUpdates []string `json:"allowed_updates"`
	}{Offset: offset, Limit: maxTelegramUpdates, Timeout: seconds, AllowedUpdates: []string{"message"}}
	var updates []telegramUpdate
	if err := b.call(ctx, "getUpdates", request, &updates); err != nil {
		return nil, err
	}
	if len(updates) > maxTelegramUpdates {
		return nil, errors.New("Telegram returned too many updates")
	}
	result := make([]Update, 0, len(updates))
	for _, update := range updates {
		if update.ID < 0 {
			return nil, errors.New("Telegram returned an invalid update ID")
		}
		result = append(result, Update{
			ID: update.ID, ChatID: update.Message.Chat.ID, Text: update.Message.Text,
		})
	}
	return result, nil
}

func (b *HTTPBot) Send(ctx context.Context, chatID int64, text string) error {
	if chatID == 0 || text == "" || len(text) > maxOutputBytes || !utf8.ValidString(text) {
		return errors.New("Telegram reply is invalid")
	}
	request := struct {
		ChatID             int64  `json:"chat_id"`
		Text               string `json:"text"`
		LinkPreviewOptions struct {
			Disabled bool `json:"is_disabled"`
		} `json:"link_preview_options"`
	}{ChatID: chatID, Text: text}
	request.LinkPreviewOptions.Disabled = true
	var result struct {
		MessageID int64 `json:"message_id"`
	}
	if err := b.call(ctx, "sendMessage", request, &result); err != nil {
		return err
	}
	if result.MessageID <= 0 {
		return errors.New("Telegram send acknowledgement is invalid")
	}
	return nil
}

func (b *HTTPBot) call(ctx context.Context, method string, requestBody, result any) error {
	encoded, err := json.Marshal(requestBody)
	if err != nil {
		return errors.New("encode Telegram request")
	}
	endpoint := b.baseURL + "/bot" + b.token + "/" + method
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(encoded))
	if err != nil {
		return errors.New("create Telegram request")
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "mithril-agent-telegram/0.1")
	response, err := b.client.Do(request)
	if err != nil {
		return errors.New("contact Telegram API")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxTelegramBodyBytes))
		return errors.New("Telegram API rejected the request")
	}
	limited := io.LimitReader(response.Body, maxTelegramBodyBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil || len(data) > maxTelegramBodyBytes {
		return errors.New("read Telegram response")
	}
	var envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&envelope); err != nil || !envelope.OK {
		return errors.New("Telegram response is invalid")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("Telegram response is invalid")
	}
	if len(envelope.Result) == 0 || string(envelope.Result) == "null" {
		return errors.New("Telegram response has no result")
	}
	if err := json.Unmarshal(envelope.Result, result); err != nil {
		return errors.New("Telegram result is invalid")
	}
	return nil
}

type telegramUpdate struct {
	ID      int64 `json:"update_id"`
	Message struct {
		Text string `json:"text"`
		Chat struct {
			ID int64 `json:"id"`
		} `json:"chat"`
	} `json:"message"`
}

func validBotToken(token string) bool {
	if len(token) < 32 || len(token) > 128 || strings.Count(token, ":") != 1 {
		return false
	}
	identifier, secret, found := strings.Cut(token, ":")
	if !found || len(identifier) < 6 || len(secret) < 20 {
		return false
	}
	if _, err := strconv.ParseUint(identifier, 10, 64); err != nil {
		return false
	}
	for _, char := range secret {
		if char < 'A' || char > 'Z' {
			if char < 'a' || char > 'z' {
				if char < '0' || char > '9' {
					if char != '_' && char != '-' {
						return false
					}
				}
			}
		}
	}
	return true
}
