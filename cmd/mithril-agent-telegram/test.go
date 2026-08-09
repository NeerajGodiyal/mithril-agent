package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Overclock-Validator/mithril-agent/telegramoperator"
)

// test answers the question the whole setup stalls on: "will I actually get
// the message?" Until now the only way to find out was to let real money move
// and see whether a phone buzzed — and every failure on the path is silent, so
// nothing arriving was indistinguishable from no trade having happened.
//
// It sends ONE fixed line to every allowed chat and reports each outcome
// separately, because the common failures are per-chat: a bot the user never
// pressed Start on, a chat ID off by a digit, an ID belonging to a group the
// bot was removed from. A single pass/fail would hide exactly the case where
// one of two chats works.
//
// It reads nothing about trades, moves no funds, and takes no arguments: the
// token comes only from the environment the service itself uses, so testing
// cannot teach an operator to paste a credential into shell history.
const testUsage = `Usage: mithril-agent-telegram test

Send one test message to every chat in MITHRIL_AGENT_TELEGRAM_CHAT_IDS and
report, per chat, whether Telegram accepted it.

Run this BEFORE trusting the bot with trade alerts. If a chat fails here it
will fail silently for a real trade.

Reads MITHRIL_AGENT_TELEGRAM_BOT_TOKEN and MITHRIL_AGENT_TELEGRAM_CHAT_IDS.
Sends nothing but the test line; never reads or reports trade state.`

// testMessage is deliberately boring and self-explaining: it may arrive on the
// phone of someone who does not know what this bot is.
const testMessage = "mithril-agent: test message. Your alerts are wired up correctly. " +
	"No trade happened and no funds moved."

// newTestBot mirrors the runTelegramService seam one file over: the delivery
// loop is the whole point of this command, so it has to be checkable without a
// real Telegram account.
var newTestBot = func(token string) (telegramoperator.Bot, error) {
	return telegramoperator.NewHTTPBot(token, &http.Client{Timeout: 30 * time.Second})
}

func runTest(ctx context.Context, output io.Writer, getenv func(string) string) error {
	token := getenv(telegramoperator.BotTokenEnvironment)
	if token == "" {
		return errors.New(telegramoperator.BotTokenEnvironment +
			" is not set; export it the same way the service receives it")
	}
	chatIDs, err := telegramoperator.ParseAllowedChatIDs(
		getenv(telegramoperator.AllowedIDsEnvironment),
	)
	if err != nil {
		return err
	}
	bot, err := newTestBot(token)
	if err != nil {
		return err
	}

	// Every chat is attempted even after one fails, for the same reason the
	// service does it: one misconfigured chat must not hide the state of the
	// others. The exit status reflects whether ALL of them worked, because a
	// partially working alert channel is not a working alert channel.
	failures := 0
	for _, chatID := range chatIDs {
		sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := bot.Send(sendCtx, chatID, testMessage)
		cancel()
		if err != nil {
			failures++
			if _, writeErr := fmt.Fprintf(output,
				"chat %d: FAILED — %s\n", chatID, explainSendFailure(err)); writeErr != nil {
				return writeErr
			}
			continue
		}
		if _, writeErr := fmt.Fprintf(output, "chat %d: delivered\n", chatID); writeErr != nil {
			return writeErr
		}
	}
	if failures != 0 {
		if _, err := fmt.Fprintln(output,
			"\nA chat that fails here will fail silently for a real trade."); err != nil {
			return err
		}
		return fmt.Errorf("%d of %d chats did not receive the test message",
			failures, len(chatIDs))
	}
	_, err = fmt.Fprintf(output,
		"\nAll %d chat(s) received the test message. Trade alerts will reach them.\n",
		len(chatIDs))
	return err
}

// explainSendFailure turns Telegram's status code into the thing to actually do.
// Telegram answers a distinct code for each common misconfiguration, and the
// bot deliberately carries only that integer out of the response — enough to
// name the cause, incapable of leaking the token or the chat's contents.
func explainSendFailure(err error) string {
	var status telegramoperator.StatusError
	if !errors.As(err, &status) {
		return err.Error()
	}
	switch status.Status {
	case 401:
		return "the bot token is not valid (HTTP 401) — check " +
			telegramoperator.BotTokenEnvironment + " against @BotFather"
	case 403:
		// Telegram refuses to let a bot open a conversation. This is the single
		// most common cause of "the bot never messages me", and it is invisible
		// from the operator's side: the setup looks complete.
		return "Telegram refused delivery (HTTP 403) — open Telegram and press " +
			"Start in this chat, or unblock the bot. A bot cannot message " +
			"anyone who has not started it"
	case 400:
		return "no such chat (HTTP 400) — the chat ID is wrong; run " +
			"`mithril-agent-telegram link` to see the IDs your bot has heard from"
	case 429:
		return "Telegram is rate limiting this bot (HTTP 429) — wait and retry"
	default:
		return status.Error()
	}
}
