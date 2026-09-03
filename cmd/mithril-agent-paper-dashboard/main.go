package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Overclock-Validator/mithril-agent/paperdashboard"
	"github.com/Overclock-Validator/mithril-agent/paperstatus"
)

const usage = "Usage: mithril-agent-paper-dashboard --paper-status-socket MARKET=/absolute/path [--optional-paper-status-socket MARKET=/absolute/path] [--instruction-path /absolute/path] [--research-packet-path /absolute/path] [--mithril-evidence-status-path /absolute/path]"

var activatedListener = systemdUnixListener

type socketPath struct {
	label string
	path  string
}

type socketPaths []socketPath

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "mithril-agent-paper-dashboard:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("mithril-agent-paper-dashboard", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var sockets, optionalSockets socketPaths
	var instructionPath, researchPath, mithrilEvidencePath, recordMithrilPath, mithrilStatus, renderInstructionPath, exportInstructionPath string
	flags.Var(&sockets, "paper-status-socket", "MARKET=/absolute/path to a bounded paper status socket")
	flags.Var(&optionalSockets, "optional-paper-status-socket", "MARKET=/absolute/path for a bounded experiment that may expire")
	flags.StringVar(&instructionPath, "instruction-path", "", "private path for a bounded paper research preference")
	flags.StringVar(&researchPath, "research-packet-path", "", "private path for the latest validated Hermes packet")
	flags.StringVar(&mithrilEvidencePath, "mithril-evidence-status-path", "", "private status from the latest Hermes Mithril evidence check")
	flags.StringVar(&recordMithrilPath, "record-mithril-evidence", "", "atomically record the latest Mithril evidence check")
	flags.StringVar(&mithrilStatus, "mithril-evidence", "", "current or unavailable")
	flags.StringVar(&renderInstructionPath, "render-instruction", "", "render a validated preference for the Hermes research prompt")
	flags.StringVar(&exportInstructionPath, "export-instruction", "", "export one validated canonical operator instruction")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			_, writeErr := fmt.Fprintln(output, usage)
			return writeErr
		}
		return err
	}
	if recordMithrilPath != "" {
		if flags.NArg() != 0 || len(sockets) != 0 || len(optionalSockets) != 0 || instructionPath != "" || researchPath != "" ||
			mithrilEvidencePath != "" || renderInstructionPath != "" || exportInstructionPath != "" ||
			!cleanAbsolutePath(recordMithrilPath) ||
			(mithrilStatus != "current" && mithrilStatus != "unavailable") {
			return errors.New("--record-mithril-evidence requires one path and current or unavailable")
		}
		return paperdashboard.RecordMithrilEvidence(
			recordMithrilPath, mithrilStatus == "current", time.Now(),
		)
	}
	if renderInstructionPath != "" {
		if flags.NArg() != 0 || len(sockets) != 0 || len(optionalSockets) != 0 || instructionPath != "" || researchPath != "" || mithrilEvidencePath != "" || mithrilStatus != "" ||
			exportInstructionPath != "" ||
			!cleanAbsolutePath(renderInstructionPath) {
			return errors.New("--render-instruction requires one clean absolute path and no server flags")
		}
		rendered, err := paperdashboard.RenderInstruction(renderInstructionPath)
		if err != nil {
			return err
		}
		_, err = fmt.Fprint(output, rendered)
		return err
	}
	if exportInstructionPath != "" {
		if flags.NArg() != 0 || len(sockets) != 0 || len(optionalSockets) != 0 || instructionPath != "" || researchPath != "" || mithrilEvidencePath != "" || mithrilStatus != "" ||
			!cleanAbsolutePath(exportInstructionPath) {
			return errors.New("--export-instruction requires one clean absolute path and no server flags")
		}
		encoded, err := paperdashboard.ExportInstruction(exportInstructionPath)
		if err != nil {
			return err
		}
		_, err = output.Write(encoded)
		return err
	}
	if flags.NArg() != 0 || len(sockets) == 0 ||
		(instructionPath != "" && !cleanAbsolutePath(instructionPath)) ||
		(researchPath != "" && !cleanAbsolutePath(researchPath)) ||
		(mithrilEvidencePath != "" && !cleanAbsolutePath(mithrilEvidencePath)) || mithrilStatus != "" {
		return errors.New("at least one --paper-status-socket is required")
	}
	for _, optional := range optionalSockets {
		for _, required := range sockets {
			if optional.label == required.label || optional.path == required.path {
				return errors.New("paper status socket labels and paths must be unique")
			}
		}
	}
	sources := make([]paperdashboard.Source, 0, len(sockets)+len(optionalSockets))
	for _, socket := range sockets {
		reader, err := paperstatus.NewLabeledSocketReader(socket.path, socket.label)
		if err != nil {
			return err
		}
		sources = append(sources, reader)
	}
	for _, socket := range optionalSockets {
		reader, err := paperstatus.NewLabeledSocketReader(socket.path, socket.label)
		if err != nil {
			return err
		}
		sources = append(sources, paperdashboard.Optional(reader))
	}
	handler, err := paperdashboard.New(sources)
	if err != nil {
		return err
	}
	if instructionPath != "" {
		if err := handler.EnableInstructions(instructionPath); err != nil {
			return err
		}
	}
	if researchPath != "" {
		if err := handler.EnableResearch(researchPath); err != nil {
			return err
		}
	}
	if mithrilEvidencePath != "" {
		if err := handler.EnableMithrilEvidence(mithrilEvidencePath); err != nil {
			return err
		}
	}
	listener, err := activatedListener()
	if err != nil {
		return err
	}
	defer listener.Close()
	server := &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8 << 10,
	}
	stopped := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = server.Shutdown(shutdownCtx)
			cancel()
		case <-stopped:
		}
	}()
	err = server.Serve(listener)
	close(stopped)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

func (paths *socketPaths) String() string {
	items := make([]string, 0, len(*paths))
	for _, item := range *paths {
		items = append(items, item.label+"="+item.path)
	}
	return strings.Join(items, ",")
}

func (paths *socketPaths) Set(value string) error {
	label, path, found := strings.Cut(value, "=")
	if !found || !validLabel(label) || !cleanAbsolutePath(path) {
		return errors.New("--paper-status-socket must be MARKET=/absolute/path")
	}
	for _, existing := range *paths {
		if existing.label == label || existing.path == path {
			return errors.New("--paper-status-socket label and path must be unique")
		}
	}
	*paths = append(*paths, socketPath{label: label, path: path})
	return nil
}

func validLabel(label string) bool {
	if label == "" || len(label) > 32 {
		return false
	}
	for _, character := range label {
		if character != '/' && character != '-' && character != '_' &&
			(character < '0' || character > '9') &&
			(character < 'A' || character > 'Z') &&
			(character < 'a' || character > 'z') {
			return false
		}
	}
	return true
}

func cleanAbsolutePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path
}

func systemdUnixListener() (net.Listener, error) {
	pid, err := strconv.ParseInt(os.Getenv("LISTEN_PID"), 10, 64)
	if err != nil || pid != int64(os.Getpid()) {
		return nil, errors.New("paper dashboard requires systemd socket activation")
	}
	fds, err := strconv.ParseUint(os.Getenv("LISTEN_FDS"), 10, 32)
	if err != nil || fds != 1 || os.Getenv("LISTEN_FDNAMES") != "paper-dashboard" {
		return nil, errors.New("paper dashboard requires exactly one named socket")
	}
	file := os.NewFile(uintptr(3), "paper-dashboard")
	if file == nil {
		return nil, errors.New("open paper dashboard socket")
	}
	listener, err := net.FileListener(file)
	_ = file.Close()
	if err != nil {
		return nil, errors.New("open paper dashboard socket")
	}
	if listener.Addr() == nil || listener.Addr().Network() != "unix" {
		_ = listener.Close()
		return nil, errors.New("paper dashboard socket is not Unix")
	}
	return listener, nil
}
