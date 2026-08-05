package swapbuilder

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/strictjson"
	"github.com/Overclock-Validator/mithril-agent/solana"
)

const quoteServerConcurrency = 4

// ServeUnix exposes a direct quote builder over a private local socket. The
// optional readiness callback runs only after the socket has mode 0660.
func ServeUnix(
	ctx context.Context,
	socketPath string,
	builder *Client,
	onReady func() error,
) error {
	if ctx == nil || builder == nil || builder.config.SocketPath != "" {
		return errors.New("direct Orca quote builder is required")
	}
	if !filepath.IsAbs(socketPath) || filepath.Clean(socketPath) != socketPath {
		return errors.New("Orca quote socket must be an absolute clean path")
	}
	parent := filepath.Dir(socketPath)
	if err := validateSocketDirectory(parent); err != nil {
		return errors.New("Orca quote socket directory is not protected")
	}
	if _, err := os.Lstat(socketPath); !errors.Is(err, os.ErrNotExist) {
		return errors.New("Orca quote socket path already exists")
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		return errors.New("listen on Orca quote socket")
	}
	defer listener.Close()
	if err := os.Chmod(socketPath, 0o660); err != nil {
		return errors.New("protect Orca quote socket")
	}
	if onReady != nil {
		if err := onReady(); err != nil {
			return errors.New("announce Orca quote service readiness")
		}
	}

	server := &http.Server{
		Handler:           newQuoteHandler(builder),
		ReadHeaderTimeout: 2 * time.Second,
		ReadTimeout:       20 * time.Second,
		WriteTimeout:      20 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    4 << 10,
	}
	serveErr := make(chan error, 1)
	go func() {
		err := server.Serve(listener)
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		serveErr <- err
	}()
	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return errors.New("stop Orca quote service")
		}
		return <-serveErr
	}
}

func newQuoteHandler(builder *Client) http.Handler {
	limit := make(chan struct{}, quoteServerConcurrency)
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.Method == http.MethodGet && request.URL.Path == "/health" &&
			request.URL.RawQuery == "" && request.ContentLength <= 0 {
			if err := builder.Health(request.Context()); err != nil {
				http.Error(writer, `{"status":"unavailable"}`, http.StatusServiceUnavailable)
				return
			}
			_, _ = io.WriteString(writer, `{"status":"ok"}`)
			return
		}
		if request.Method != http.MethodPost || request.URL.Path != "/quote" ||
			request.URL.RawQuery != "" ||
			request.Header.Get("Content-Type") != "application/json" {
			http.Error(writer, `{"error":"invalid_request"}`, http.StatusBadRequest)
			return
		}
		select {
		case limit <- struct{}{}:
			defer func() { <-limit }()
		default:
			http.Error(writer, `{"error":"busy"}`, http.StatusServiceUnavailable)
			return
		}
		data, err := io.ReadAll(io.LimitReader(request.Body, maxBuilderRequestBytes+1))
		if err != nil || len(data) > maxBuilderRequestBytes {
			http.Error(writer, `{"error":"invalid_request"}`, http.StatusBadRequest)
			return
		}
		quoteRequest, err := decodeWireRequest(data)
		if err != nil {
			http.Error(writer, `{"error":"invalid_request"}`, http.StatusBadRequest)
			return
		}
		result, err := builder.Quote(request.Context(), quoteRequest)
		if err != nil {
			status := http.StatusBadGateway
			if errors.Is(err, ErrQuoteTemporarilyUnavailable) {
				status = http.StatusServiceUnavailable
			}
			http.Error(writer, `{"error":"quote_failed"}`, status)
			return
		}
		encoded, err := encodeResult(result)
		if err != nil {
			http.Error(writer, `{"error":"quote_failed"}`, http.StatusBadGateway)
			return
		}
		_, _ = writer.Write(encoded)
	})
}

func decodeWireRequest(data []byte) (Request, error) {
	var wire wireRequest
	if err := strictjson.Decode(data, &wire); err != nil {
		return Request{}, errors.New("decode Orca quote request")
	}
	amount, err := strconv.ParseUint(wire.InputAmount, 10, 64)
	if err != nil {
		return Request{}, errors.New("decode Orca quote amount")
	}
	return Request{
		Owner: wire.Owner, Pool: wire.Pool, InputMint: wire.InputMint,
		InputAmount: amount, SlippageBPS: wire.SlippageBPS,
	}, nil
}

func encodeResult(result Result) ([]byte, error) {
	wire := wireResult{
		Instructions:         make([]wireInstruction, len(result.Instructions)),
		TokenIn:              strconv.FormatUint(result.TokenIn, 10),
		TokenEstOut:          strconv.FormatUint(result.TokenEstOut, 10),
		TokenMinOut:          strconv.FormatUint(result.TokenMinOut, 10),
		TradeEnableTimestamp: strconv.FormatInt(result.TradeEnableTimestamp.Unix(), 10),
	}
	for index, instruction := range result.Instructions {
		wire.Instructions[index] = wireInstruction{
			Program:    instruction.Program,
			Accounts:   append([]solana.AccountMeta(nil), instruction.Accounts...),
			DataBase64: base64.StdEncoding.EncodeToString(instruction.Data),
		}
	}
	return json.Marshal(wire)
}
