// Package upstream talks to the M3 site of the Nemzeti Archivum.
//
// Everything that comes back from it is untrusted input: sizes are capped, status
// codes are checked, JSON is validated and the redirect targets are restricted to
// the one configured https host.
package upstream

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// DefaultBaseURL is the only host the program talks to in production.
const DefaultBaseURL = "https://nemzetiarchivum.hu"

const (
	defaultMaxBody = 32 << 20 // 32 MiB
	maxRedirects   = 3
)

// ErrNotFound means the upstream knows nothing about the requested item.
var ErrNotFound = errors.New("upstream: not found")

// StatusError is returned for any non-2xx response.
type StatusError struct{ Code int }

func (e *StatusError) Error() string {
	return fmt.Sprintf("upstream: unexpected HTTP status %d", e.Code)
}

// Options configures a Client.
type Options struct {
	// BaseURL must be https. Defaults to DefaultBaseURL. Tests point it at a local TLS server.
	BaseURL string
	// UserAgent is sent on every request (including the proxy CONNECT).
	UserAgent string
	// Proxy routes everything through an http:// or https:// CONNECT proxy. Nil = direct connection.
	Proxy *url.URL
	// ProxyInsecure skips verification of the proxy's own TLS certificate (https proxies only).
	ProxyInsecure bool
	// RootCAs are the trusted roots for the upstream site. Nil = system roots.
	RootCAs *x509.CertPool
	// Timeout is the total time budget of one request. Default 60 s.
	Timeout time.Duration
	// MaxBody caps a response body. Default 32 MiB.
	MaxBody int64
	// Delay is the minimum pause between two consecutive requests.
	Delay time.Duration
}

// Client is a restricted HTTP client for one https host. It is safe for concurrent use,
// although the ingest uses it sequentially.
type Client struct {
	base    *url.URL
	ua      string
	hc      *http.Client
	maxBody int64
	delay   time.Duration

	mu   sync.Mutex
	last time.Time
}

// NewClient validates the options and builds the client.
func NewClient(o Options) (*Client, error) {
	if o.BaseURL == "" {
		o.BaseURL = DefaultBaseURL
	}
	base, err := url.Parse(o.BaseURL)
	if err != nil || base.Scheme != "https" || base.Hostname() == "" || base.User != nil {
		return nil, errors.New("upstream: BaseURL must be a plain https URL")
	}
	if o.UserAgent == "" {
		return nil, errors.New("upstream: a User-Agent is required")
	}
	if o.Timeout <= 0 {
		o.Timeout = 60 * time.Second
	}
	if o.MaxBody <= 0 {
		o.MaxBody = defaultMaxBody
	}

	var dial func(ctx context.Context, network, addr string) (net.Conn, error)
	if o.Proxy != nil {
		if o.Proxy.Scheme != "http" && o.Proxy.Scheme != "https" {
			return nil, errors.New("upstream: proxy scheme must be http or https")
		}
		dial = newProxyDialer(o.Proxy, o.ProxyInsecure, o.UserAgent).DialContext
	} else {
		d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
		dial = d.DialContext
	}

	tr := &http.Transport{
		// Environment proxy variables are deliberately NOT honoured: the only way to
		// use a proxy is the explicit UPSTREAM_PROXY setting.
		Proxy:                 nil,
		DialContext:           dial,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: o.RootCAs},
		ForceAttemptHTTP2:     true,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns:          2,
		IdleConnTimeout:       30 * time.Second,
	}

	c := &Client{base: base, ua: o.UserAgent, maxBody: o.MaxBody, delay: o.Delay}
	c.hc = &http.Client{
		Transport: tr,
		Timeout:   o.Timeout,
		// no cookie jar: the upstream's cookies are never stored or replayed
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > maxRedirects {
				return errors.New("upstream: too many redirects")
			}
			if req.URL.Scheme != "https" || req.URL.Host != c.base.Host {
				return errors.New("upstream: redirect to a different host refused")
			}
			return nil
		},
	}
	return c, nil
}

// CloseIdleConnections releases pooled connections.
func (c *Client) CloseIdleConnections() { c.hc.CloseIdleConnections() }

// url builds an absolute URL on the allowed host.
func (c *Client) url(path string, q url.Values) *url.URL {
	u := *c.base
	u.Path = path
	u.RawQuery = q.Encode()
	return &u
}

// throttle enforces the minimum delay between requests.
func (c *Client) throttle(ctx context.Context) error {
	if c.delay <= 0 {
		return ctx.Err()
	}
	c.mu.Lock()
	wait := time.Until(c.last.Add(c.delay))
	c.last = time.Now().Add(max(wait, 0))
	c.mu.Unlock()
	if wait <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(wait)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// get performs a GET and returns the body of a 2xx response, at most limit bytes.
func (c *Client) get(ctx context.Context, u *url.URL, accept string, limit int64) ([]byte, error) {
	if u.Scheme != "https" || u.Host != c.base.Host {
		return nil, errors.New("upstream: URL outside the allowed host")
	}
	if limit <= 0 || limit > c.maxBody {
		limit = c.maxBody
	}
	if err := c.throttle(ctx); err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.ua)
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Language", "hu-HU,hu;q=0.9,en-US;q=0.8,en;q=0.7")

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("upstream: %w", scrub(err))
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return nil, &StatusError{Code: resp.StatusCode}
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil {
		return nil, fmt.Errorf("upstream: reading body: %w", scrub(err))
	}
	if int64(len(body)) > limit {
		return nil, fmt.Errorf("upstream: response larger than %d bytes", limit)
	}
	return body, nil
}

// scrub removes the request URL that net/http puts into *url.Error: the URL may contain
// nothing secret here, but the message stays short and uniform.
func scrub(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		return ue.Err
	}
	return err
}
