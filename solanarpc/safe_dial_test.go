package solanarpc

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"testing"
)

func TestProtectedDialRejectsEveryRestrictedResolution(t *testing.T) {
	tests := []string{
		"0.0.0.0", "0.0.0.1", "127.0.0.1", "10.0.0.1", "100.64.0.1", "169.254.169.254",
		"172.16.0.1", "192.168.0.1", "198.18.0.1", "224.0.0.1", "::", "::1",
		"240.0.0.1", "fc00::1", "fec0::1", "fe80::1", "ff02::1", "::ffff:127.0.0.1",
	}
	for _, value := range tests {
		t.Run(value, func(t *testing.T) {
			dialed := false
			dial := protectedDialContext(
				func(context.Context, string, string) ([]netip.Addr, error) {
					return []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr(value)}, nil
				},
				func(context.Context, string, string) (net.Conn, error) {
					dialed = true
					return nil, errors.New("unexpected dial")
				},
			)
			if _, err := dial(t.Context(), "tcp", "provider.example:443"); err == nil || dialed {
				t.Fatalf("restricted resolution was accepted: err=%v dialed=%v", err, dialed)
			}
		})
	}
}

func TestProtectedDialPinsValidatedAddress(t *testing.T) {
	var destination string
	want := netip.MustParseAddr("2606:4700:4700::1111")
	dial := protectedDialContext(
		func(context.Context, string, string) ([]netip.Addr, error) {
			return []netip.Addr{want}, nil
		},
		func(_ context.Context, network, address string) (net.Conn, error) {
			if network != "tcp" {
				t.Fatalf("network = %q", network)
			}
			destination = address
			return nil, errors.New("test dial complete")
		},
	)
	if _, err := dial(t.Context(), "tcp", "provider.example:443"); err == nil {
		// The fake dialer deliberately returns an error after recording its target.
		t.Fatal("fake dial unexpectedly succeeded")
	}
	if destination != "[2606:4700:4700::1111]:443" {
		t.Fatalf("destination = %q", destination)
	}
}

func TestProtectedDialRejectsResolutionFailure(t *testing.T) {
	for name, lookup := range map[string]lookupNetIPFunc{
		"error": func(context.Context, string, string) ([]netip.Addr, error) {
			return nil, errors.New("lookup failed")
		},
		"empty": func(context.Context, string, string) ([]netip.Addr, error) {
			return nil, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			dial := protectedDialContext(lookup, nil)
			if _, err := dial(t.Context(), "tcp", "provider.example:443"); err == nil {
				t.Fatal("invalid resolution was accepted")
			}
		})
	}
}
