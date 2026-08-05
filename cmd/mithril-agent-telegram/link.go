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

// link answers the one question every Telegram setup stalls on: "what is my
// chat ID?" Telegram never shows it, so the operator messages the bot and
// this command reads the pending updates and prints the IDs it saw.
//
// It is strictly read-and-discard. It never sends anything, never confirms an
// update offset, and never touches the service's cursor file — so every
// update it reads is still there, unconsumed, for the service to process.
// The token comes only from the same environment variable the service uses;
// accepting it as an argument would teach operators to put the credential in
// shell history.
const linkUsage = `Usage: mithril-agent-telegram link

Discover your Telegram chat ID:
  1. Open Telegram and send any message to your bot (say: hello)
  2. Run this command within a day of sending it
  3. Put the printed chat ID into MITHRIL_AGENT_TELEGRAM_CHAT_IDS

Reads MITHRIL_AGENT_TELEGRAM_BOT_TOKEN from the environment. Read-only: it
consumes nothing, sends nothing, and leaves every update for the service.`

func runLink(ctx context.Context, output io.Writer, getenv func(string) string) error {
	token := getenv("MITHRIL_AGENT_TELEGRAM_BOT_TOKEN")
	if token == "" {
		return errors.New("MITHRIL_AGENT_TELEGRAM_BOT_TOKEN is not set; export it the same way the service receives it")
	}
	bot, err := telegramoperator.NewHTTPBot(token, &http.Client{Timeout: 30 * time.Second})
	if err != nil {
		return err
	}
	// Offset zero asks for every unconfirmed update without confirming any.
	updates, err := bot.Poll(ctx, 0, 5*time.Second)
	if err != nil {
		return errors.New("could not reach Telegram; check the token and network")
	}
	if len(updates) == 0 {
		_, err := fmt.Fprintln(output,
			"No pending messages. Send your bot any message from the account\n"+
				"you want linked, then run this again. (Messages already processed\n"+
				"by a running service will not appear; stop it first, or send a\n"+
				"fresh message.)")
		return err
	}
	// One line per distinct chat, newest last. The message text is
	// deliberately NOT printed: it is user content, and the only thing the
	// operator needs is the number.
	seen := make(map[int64]int)
	order := make([]int64, 0, len(updates))
	for _, update := range updates {
		if _, ok := seen[update.ChatID]; !ok {
			order = append(order, update.ChatID)
		}
		seen[update.ChatID]++
	}
	for _, chatID := range order {
		if _, err := fmt.Fprintf(output, "chat ID %d (%d pending message(s))\n", chatID, seen[chatID]); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintln(output,
		"\nAdd yours to the service environment:\n  MITHRIL_AGENT_TELEGRAM_CHAT_IDS=<your chat ID>\n"+
			"Only listed IDs ever receive a reply; everyone else is ignored silently.")
	return err
}
