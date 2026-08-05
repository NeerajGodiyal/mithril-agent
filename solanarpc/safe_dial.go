package solanarpc

import (
	"context"
	"errors"
	"net"
	"net/netip"
)

type lookupNetIPFunc func(context.Context, string, string) ([]netip.Addr, error)
type dialContextFunc func(context.Context, string, string) (net.Conn, error)

func externalRPCDialContext() dialContextFunc {
	dialer := &net.Dialer{}
	return protectedDialContext(net.DefaultResolver.LookupNetIP, dialer.DialContext)
}

func protectedDialContext(lookup lookupNetIPFunc, dial dialContextFunc) dialContextFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil || host == "" || port == "" {
			return nil, errors.New("RPC destination is invalid")
		}
		addresses, err := lookup(ctx, "ip", host)
		if err != nil || len(addresses) == 0 {
			return nil, errors.New("resolve RPC destination")
		}
		for _, resolved := range addresses {
			if !publicRPCAddress(resolved) {
				return nil, errors.New("RPC destination resolved to a restricted address")
			}
		}
		var lastErr error
		for _, resolved := range addresses {
			connection, err := dial(ctx, network, net.JoinHostPort(resolved.String(), port))
			if err == nil {
				return connection, nil
			}
			lastErr = err
		}
		if lastErr != nil {
			return nil, errors.New("connect to RPC destination")
		}
		return nil, errors.New("RPC destination is unavailable")
	}
}

func publicRPCAddress(address netip.Addr) bool {
	if !address.IsValid() {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() ||
		address.IsLinkLocalUnicast() || address.IsLinkLocalMulticast() ||
		address.IsMulticast() || address.IsUnspecified() {
		return false
	}
	for _, prefix := range []netip.Prefix{
		netip.MustParsePrefix("0.0.0.0/8"),
		netip.MustParsePrefix("100.64.0.0/10"),
		netip.MustParsePrefix("192.0.0.0/24"),
		netip.MustParsePrefix("198.18.0.0/15"),
		netip.MustParsePrefix("240.0.0.0/4"),
		netip.MustParsePrefix("fec0::/10"),
	} {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}
