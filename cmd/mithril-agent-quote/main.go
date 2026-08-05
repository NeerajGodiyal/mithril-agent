package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

	"github.com/Overclock-Validator/mithril-agent/swapbuilder"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "mithril-agent-quote:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("mithril-agent-quote", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	socketPath := flags.String("socket", "", "private Unix socket path")
	nodeCommand := flags.String("node-command", "", "absolute Node.js executable")
	quoteScript := flags.String("quote-script", "", "absolute Orca quote adapter")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(
				output,
				"Usage: mithril-agent-quote --socket PATH --node-command PATH --quote-script PATH",
			)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *socketPath == "" || *nodeCommand == "" || *quoteScript == "" {
		return errors.New("socket, Node.js command, and quote script are required")
	}
	builder, err := swapbuilder.New(swapbuilder.Config{
		NodeCommand: *nodeCommand,
		ScriptPath:  *quoteScript,
		RPCURL:      os.Getenv("MITHRIL_AGENT_QUOTE_RPC_URL"),
	})
	if err != nil {
		return err
	}
	if err := builder.SelfTest(ctx); err != nil {
		return err
	}
	return swapbuilder.ServeUnix(ctx, *socketPath, builder, notifySystemdReady)
}

func notifySystemdReady() error {
	path := os.Getenv("NOTIFY_SOCKET")
	if path == "" {
		return nil
	}
	if path[0] == '@' {
		path = "\x00" + path[1:]
	}
	connection, err := net.DialUnix(
		"unixgram",
		nil,
		&net.UnixAddr{Name: path, Net: "unixgram"},
	)
	if err != nil {
		return errors.New("connect to systemd notification socket")
	}
	defer connection.Close()
	if _, err := connection.Write([]byte("READY=1")); err != nil {
		return errors.New("notify systemd that quote service is ready")
	}
	return nil
}
