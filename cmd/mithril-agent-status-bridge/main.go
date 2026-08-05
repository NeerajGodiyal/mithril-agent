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
	"strconv"
	"syscall"

	"github.com/Overclock-Validator/mithril-agent/statussocket"
)

const (
	usage                = "Usage: mithril-agent-status-bridge --credential operator-status"
	statusCredentialName = "operator-status"
)

var activatedListener = systemdUnixListener

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "mithril-agent-status-bridge:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("mithril-agent-status-bridge", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	credential := flags.String("credential", statusCredentialName, "fixed systemd status credential")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, usage)
			return writeErr
		}
		return err
	}
	if flags.NArg() != 0 || *credential != statusCredentialName {
		return errors.New("--credential must be operator-status")
	}
	reader, err := statussocket.NewCredentialReader(
		os.Getenv("CREDENTIALS_DIRECTORY"), *credential,
	)
	if err != nil {
		return err
	}
	listener, err := activatedListener()
	if err != nil {
		return err
	}
	defer listener.Close()
	return statussocket.Serve(ctx, listener, reader)
}

func systemdUnixListener() (net.Listener, error) {
	pid, err := strconv.ParseInt(os.Getenv("LISTEN_PID"), 10, 64)
	if err != nil || pid != int64(os.Getpid()) {
		return nil, errors.New("status bridge requires systemd socket activation")
	}
	fds, err := strconv.ParseUint(os.Getenv("LISTEN_FDS"), 10, 32)
	if err != nil || fds != 1 {
		return nil, errors.New("status bridge requires exactly one activated socket")
	}
	if names := os.Getenv("LISTEN_FDNAMES"); names != "operator-status" {
		return nil, errors.New("status bridge activated socket name is invalid")
	}
	file := os.NewFile(uintptr(3), "operator-status")
	if file == nil {
		return nil, errors.New("open activated status socket")
	}
	listener, err := net.FileListener(file)
	_ = file.Close()
	if err != nil {
		return nil, errors.New("open activated status socket")
	}
	if listener.Addr() == nil || listener.Addr().Network() != "unix" {
		_ = listener.Close()
		return nil, errors.New("activated status socket is not Unix")
	}
	return listener, nil
}
