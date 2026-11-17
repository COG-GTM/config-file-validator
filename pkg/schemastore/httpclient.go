package schemastore

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"sync"
	"syscall"
	"time"
)

const maxRedirects = 5

// ErrBlockedURL is returned when a schema URL, redirect target, or resolved
// address is rejected by the SchemaStore fetch policy.
var ErrBlockedURL = errors.New("schema URL blocked")

// Address ranges that are not covered by the netip.Addr helper methods but
// must never be reached from a catalog URL.
var blockedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
}

// checkPublicAddr rejects loopback, private, link-local, multicast,
// unspecified, and other non-public addresses.
func checkPublicAddr(addr netip.AddrPort) error {
	ip := addr.Addr().Unmap()
	if !ip.IsValid() || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return fmt.Errorf("%w: non-public address %s", ErrBlockedURL, ip)
	}
	for _, p := range blockedPrefixes {
		if p.Contains(ip) {
			return fmt.Errorf("%w: non-public address %s", ErrBlockedURL, ip)
		}
	}
	return nil
}

// checkSchemaURL accepts only absolute https URLs with a host.
func checkSchemaURL(u *url.URL) error {
	if u.Scheme != "https" {
		return fmt.Errorf("%w: scheme %q is not https", ErrBlockedURL, u.Scheme)
	}
	if u.Hostname() == "" {
		return fmt.Errorf("%w: missing host", ErrBlockedURL)
	}
	return nil
}

// newSchemaClient returns an HTTP client for fetching catalog schemas. Every
// connection is checked against checkAddr after name resolution, and every
// redirect target must pass checkSchemaURL. When a proxy is configured, the
// target host is resolved and checked before the request is sent to the proxy.
func newSchemaClient(checkAddr func(netip.AddrPort) error) *http.Client {
	dialer := &net.Dialer{
		Timeout:   fetchTimeout,
		KeepAlive: 30 * time.Second,
		Control: func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil {
				return fmt.Errorf("%w: %w", ErrBlockedURL, err)
			}
			return checkAddr(ap)
		},
	}
	proxyDialer := &net.Dialer{Timeout: fetchTimeout, KeepAlive: 30 * time.Second}

	var proxies sync.Map
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = func(req *http.Request) (*url.URL, error) {
		proxyURL, err := http.ProxyFromEnvironment(req)
		if err != nil || proxyURL == nil {
			return proxyURL, err
		}
		if err := checkResolvedHost(req.Context(), req.URL, checkAddr); err != nil {
			return nil, err
		}
		proxies.Store(proxyURL.Host, struct{}{})
		return proxyURL, nil
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if _, ok := proxies.Load(address); ok {
			return proxyDialer.DialContext(ctx, network, address)
		}
		return dialer.DialContext(ctx, network, address)
	}

	return &http.Client{
		Transport: transport,
		Timeout:   fetchTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= maxRedirects {
				return fmt.Errorf("%w: stopped after %d redirects", ErrBlockedURL, maxRedirects)
			}
			return checkSchemaURL(req.URL)
		},
	}
}

// checkResolvedHost resolves the URL host and checks every address. It is used
// for proxied requests, where the connection itself goes to the proxy.
func checkResolvedHost(ctx context.Context, u *url.URL, checkAddr func(netip.AddrPort) error) error {
	port := uint16(443)
	if p, err := strconv.ParseUint(u.Port(), 10, 16); err == nil {
		port = uint16(p)
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", u.Hostname())
	if err != nil {
		return fmt.Errorf("resolving schema host: %w", err)
	}
	for _, a := range addrs {
		if err := checkAddr(netip.AddrPortFrom(a, port)); err != nil {
			return err
		}
	}
	return nil
}

var defaultSchemaClient = newSchemaClient(checkPublicAddr)
