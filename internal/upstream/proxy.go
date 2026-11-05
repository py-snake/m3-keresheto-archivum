package upstream

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"
)

// proxyDialer opens TCP tunnels through an HTTP or HTTPS proxy with the CONNECT method.
//
// Only the tunnel to the proxy can be configured as "insecure" (certificate not
// verified, for self-signed proxies). The TLS session to the upstream site itself is
// set up by http.Transport *inside* the tunnel and is always verified.
//
// The destination host name is sent to the proxy unresolved, so DNS resolution
// happens at the proxy, not on this machine.
type proxyDialer struct {
	proxy     *url.URL
	insecure  bool
	userAgent string
	dialer    net.Dialer
}

const connectTimeout = 30 * time.Second

func newProxyDialer(proxy *url.URL, insecure bool, userAgent string) *proxyDialer {
	return &proxyDialer{
		proxy:     proxy,
		insecure:  insecure,
		userAgent: userAgent,
		dialer:    net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second},
	}
}

// label names the proxy in error messages: scheme and host only, never any credentials.
func (d *proxyDialer) label() string { return d.proxy.Scheme + "://" + d.proxy.Host }

func (d *proxyDialer) proxyAddr() string {
	if d.proxy.Port() != "" {
		return d.proxy.Host
	}
	if d.proxy.Scheme == "https" {
		return net.JoinHostPort(d.proxy.Hostname(), "443")
	}
	return net.JoinHostPort(d.proxy.Hostname(), "80")
}

// DialContext matches http.Transport.DialContext.
func (d *proxyDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	switch network {
	case "tcp", "tcp4", "tcp6":
	default:
		return nil, fmt.Errorf("proxy dialer: unsupported network %q", network)
	}

	conn, err := d.dialer.DialContext(ctx, "tcp", d.proxyAddr())
	if err != nil {
		return nil, fmt.Errorf("connecting to proxy %s: %w", d.label(), err)
	}

	// Abort a stuck handshake / CONNECT exchange when the request context ends.
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	if d.proxy.Scheme == "https" {
		tc := tls.Client(conn, &tls.Config{
			ServerName: d.proxy.Hostname(),
			MinVersion: tls.VersionTLS12,
			// Opt-in only (UPSTREAM_PROXY_INSECURE): lets a self-signed proxy certificate through.
			// This never applies to the upstream site's certificate.
			InsecureSkipVerify: d.insecure, //nolint:gosec
		})
		if err := tc.HandshakeContext(ctx); err != nil {
			_ = conn.Close()
			return nil, fmt.Errorf("TLS handshake with proxy %s: %w", d.label(), err)
		}
		conn = tc
	}

	deadline := time.Now().Add(connectTimeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	_ = conn.SetDeadline(deadline)

	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: addr},
		Host:   addr,
		Header: make(http.Header),
	}
	req.Header.Set("User-Agent", d.userAgent)
	if u := d.proxy.User; u != nil {
		pass, _ := u.Password()
		cred := base64.StdEncoding.EncodeToString([]byte(u.Username() + ":" + pass))
		req.Header.Set("Proxy-Authorization", "Basic "+cred)
	}
	if err := req.Write(conn); err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("sending CONNECT to proxy %s: %w", d.label(), err)
	}

	br := bufio.NewReader(conn)
	resp, err := http.ReadResponse(br, req)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("reading CONNECT response from proxy %s: %w", d.label(), err)
	}
	if resp.StatusCode != http.StatusOK {
		_ = conn.Close()
		// only the status line: the response body may echo anything
		return nil, fmt.Errorf("proxy %s refused CONNECT: %s", d.label(), resp.Status)
	}

	_ = conn.SetDeadline(time.Time{})
	if br.Buffered() > 0 {
		return &bufferedConn{Conn: conn, r: br}, nil
	}
	return conn, nil
}

// bufferedConn returns bytes the bufio reader already pulled from the connection first.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
