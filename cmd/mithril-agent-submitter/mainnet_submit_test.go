package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Overclock-Validator/mithril-agent/internal/control"
	"github.com/Overclock-Validator/mithril-agent/solanarpc"
	"github.com/Overclock-Validator/mithril-agent/submitter"
)

type unreadMainnetInput struct{ reads int }

func (r *unreadMainnetInput) Read([]byte) (int, error) {
	r.reads++
	return 0, errors.New("unexpected unsigned input read")
}

func TestSubmitMainnetCLIRejectsUnrelatedAuthority(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "policy.json")
	writePrivateJSON(t, path, mainnetSubmitterPolicy(t, dir))
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{"key", []string{"--key", filepath.Join(dir, "must-not-read-key")}, "--key"},
		{"check", []string{"--check-mainnet"}, "mutually exclusive"},
		{"prepare", []string{"--prepare-mainnet"}, "mutually exclusive"},
		{"socket", []string{"--socket"}, "mutually exclusive"},
		{"operator", []string{"--operator-socket"}, "mutually exclusive"},
		{"recover", []string{"--recover"}, "mutually exclusive"},
		{"request", []string{"--signer-request", "/unread/request", "--signer-response", "/unread/response"}, "--prepare-mainnet"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := &unreadMainnetInput{}
			var output bytes.Buffer
			args := append([]string{"--policy", path, "--submit-mainnet"}, test.args...)
			err := run(t.Context(), args, input, &output)
			if err == nil || !strings.Contains(err.Error(), test.want) || input.reads != 0 || output.Len() != 0 {
				t.Fatalf("refusal=%v reads=%d output=%q", err, input.reads, output.String())
			}
		})
	}
	policy := mainnetSubmitterPolicy(t, dir)
	policy.Cluster, policy.Jupiter = "devnet", nil
	writePrivateJSON(t, path, policy)
	input := &unreadMainnetInput{}
	var output bytes.Buffer
	if err := run(t.Context(), []string{"--policy", path, "--submit-mainnet"}, input, &output); err == nil || !strings.Contains(err.Error(), "requires a Jupiter Mainnet policy") || input.reads != 0 || output.Len() != 0 {
		t.Fatalf("non-Mainnet refusal=%v reads=%d", err, input.reads)
	}
}

func TestSubmitMainnetCLIStoppedAndMismatchedProvidersNeverDial(t *testing.T) {
	// Intercept both local HTTP and external HTTPS to prevent any network request.
	// Keep the concrete transport type: production clones it before use.
	original := http.DefaultTransport
	transport := original.(*http.Transport).Clone()
	var dials atomic.Int64
	denyDial := func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("offline test forbids network")
	}
	transport.DialContext = denyDial
	transport.DialTLSContext = denyDial
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = original; transport.CloseIdleConnections() })
	const primaryURL = "https://primary.invalid"
	const secondaryURL = "https://secondary.invalid"
	t.Setenv("MITHRIL_AGENT_PRIMARY_RPC_URL", primaryURL)
	t.Setenv("MITHRIL_AGENT_SECONDARY_RPC_URL", secondaryURL)
	t.Setenv("MITHRIL_AGENT_MITHRIL_RPC_URL", "http://127.0.0.1:8899")
	for _, mismatch := range []bool{false, true} {
		t.Run(map[bool]string{false: "stopped", true: "provider mismatch"}[mismatch], func(t *testing.T) {
			dir, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(dir, 0700); err != nil {
				t.Fatal(err)
			}
			policy := mainnetSubmitterPolicy(t, dir)
			primary, err := solanarpc.NewPaced(primaryURL, nil, 750*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			secondary, err := solanarpc.NewPaced(secondaryURL, nil, 750*time.Millisecond)
			if err != nil {
				t.Fatal(err)
			}
			policy.Evidence.PrimaryOriginSHA256 = primary.Identity()
			policy.Evidence.SecondaryOriginSHA256 = secondary.Identity()
			if mismatch {
				policy.Evidence.PrimaryOriginSHA256 = strings.Repeat("a", 64)
			}
			gate, err := control.NewMainnetCanaryStateFile(policy.ControlStatePath, policy.ProfileFingerprint, false)
			if err != nil {
				t.Fatal(err)
			}
			if err := gate.Stop("offline CLI test"); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(policy.ControlStatePath)
			if err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "policy.json")
			writePrivateJSON(t, path, policy)
			input := &unreadMainnetInput{}
			var output bytes.Buffer
			err = run(t.Context(), []string{"--policy", path, "--submit-mainnet"}, input, &output)
			if mismatch {
				if err == nil || !strings.Contains(err.Error(), "evidence RPCs do not match") {
					t.Fatalf("provider mismatch=%v", err)
				}
			} else if !errors.Is(err, submitter.ErrControlBlocked) {
				t.Fatalf("stopped control=%v", err)
			}
			after, readErr := os.ReadFile(policy.ControlStatePath)
			if readErr != nil || !bytes.Equal(before, after) || dials.Load() != 0 || input.reads != 0 || output.Len() != 0 {
				t.Fatalf("refusal mutated control or performed IO: read=%v dials=%d input=%d", readErr, dials.Load(), input.reads)
			}
		})
	}
}

func TestSubmitMainnetCLIHelp(t *testing.T) {
	var output bytes.Buffer
	if err := run(t.Context(), []string{"--help"}, io.LimitReader(&unreadMainnetInput{}, 0), &output); err != nil || !strings.Contains(output.String(), "--submit-mainnet") {
		t.Fatalf("help=%q error=%v", output.String(), err)
	}
}
