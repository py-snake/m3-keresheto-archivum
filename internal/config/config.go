// Package config reads the whole runtime configuration from environment variables.
//
// Nothing else in the program touches the environment, and there are no secrets
// except an optional proxy password inside UPSTREAM_PROXY.
package config

import (
	"fmt"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// DefaultUserAgent is a regular desktop browser User-Agent. It is a deliberate
// project decision that outbound requests never announce a custom client name.
// Keep the version reasonably current and override it with UPSTREAM_USER_AGENT.
const DefaultUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/155.0.0.0 Safari/537.36"

// Config is the complete runtime configuration.
type Config struct {
	// DataDir holds m3.sqlite, images/ and backups/.
	DataDir string
	// Listen is the address `m3 serve` listens on.
	Listen string
	// TrustedProxies are the networks whose X-Forwarded-For / CF-Connecting-IP headers are believed.
	TrustedProxies []netip.Prefix

	// RateLimit is the sustained requests per second allowed per client, RateBurst the burst size.
	RateLimit float64
	RateBurst int
	// MaxInFlight caps the number of requests handled concurrently.
	MaxInFlight int

	// UpstreamUserAgent is sent on every outbound request.
	UpstreamUserAgent string
	// UpstreamProxy routes all outbound traffic through an HTTP or HTTPS (CONNECT) proxy. Nil = direct.
	UpstreamProxy *url.URL
	// UpstreamProxyInsecure skips verification of the *proxy's* TLS certificate (https:// proxies only).
	// The certificate of the upstream site itself is always verified.
	UpstreamProxyInsecure bool
	// UpstreamCAFile adds extra trusted root certificates (PEM) for the upstream site.
	UpstreamCAFile string
	// RequestDelay is the pause between consecutive upstream requests of the ingest.
	RequestDelay time.Duration
	// ImagesPerRun caps how many cover images one ingest run downloads.
	ImagesPerRun int
	// ImageMaxWidth is the width cover images are scaled down to (0 = keep the original size).
	ImageMaxWidth int
	// BackupsKept is the number of rotated database backups.
	BackupsKept int
	// MaxRequests is the hard ceiling of upstream requests per ingest process.
	MaxRequests int
	// RecheckPerRun is how many stored programs `ingest recheck` re-queries per run. 0 = disabled
	// (the default): availability is then only refreshed by requests the other jobs send anyway.
	RecheckPerRun int
	// RecheckDelay is the pause between two recheck requests.
	RecheckDelay time.Duration
	// PublicURL is the public address of the site (scheme://host, no path), e.g. https://m3.example.hu.
	// It is what absolute links are built from: the canonical address, the sitemap and the link
	// previews. Empty = not set: the site then asks search engines not to index it and has no sitemap.
	PublicURL string
	// LockWait is how long an ingest or export waits for another one to finish (0 = fail at once).
	LockWait time.Duration
	// PublicAPI / PublicExport switch the REST API and the public downloads on (both are on by default).
	PublicAPI, PublicExport bool
	// RefreshPerRun is how many stored programs `ingest refresh` re-fetches per run to update their
	// descriptive data. 0 = disabled (the default).
	RefreshPerRun int
	// RefreshDelay is the pause between two refresh requests.
	RefreshDelay time.Duration
}

// FromEnv builds a Config from getenv (usually os.Getenv).
func FromEnv(getenv func(string) string) (*Config, error) {
	c := &Config{
		DataDir:           str(getenv, "M3_DATA_DIR", "/data"),
		Listen:            str(getenv, "M3_LISTEN", ":8080"),
		RateLimit:         3,
		RateBurst:         15,
		MaxInFlight:       64,
		UpstreamUserAgent: str(getenv, "UPSTREAM_USER_AGENT", DefaultUserAgent),
		UpstreamCAFile:    getenv("UPSTREAM_CA_FILE"),
		RequestDelay:      250 * time.Millisecond,
		ImagesPerRun:      300,
		ImageMaxWidth:     480,
		BackupsKept:       14,
		MaxRequests:       2000,
		RecheckPerRun:     0,
		RecheckDelay:      3 * time.Second,
		LockWait:          10 * time.Minute,
		RefreshPerRun:     0,
		RefreshDelay:      3 * time.Second,
		PublicAPI:         true,
		PublicExport:      true,
	}

	var err error

	if c.TrustedProxies, err = parsePrefixes(getenv("M3_TRUSTED_PROXIES")); err != nil {
		return nil, fmt.Errorf("M3_TRUSTED_PROXIES: %w", err)
	}

	if v := getenv("UPSTREAM_PROXY"); v != "" {
		if c.UpstreamProxy, err = ParseProxyURL(v); err != nil {
			// never echo the value: it may contain a password
			return nil, fmt.Errorf("UPSTREAM_PROXY: %w", err)
		}
	}

	if c.UpstreamProxyInsecure, err = boolean(getenv("UPSTREAM_PROXY_INSECURE")); err != nil {
		return nil, fmt.Errorf("UPSTREAM_PROXY_INSECURE: %w", err)
	}
	if c.UpstreamProxyInsecure && (c.UpstreamProxy == nil || c.UpstreamProxy.Scheme != "https") {
		return nil, fmt.Errorf("UPSTREAM_PROXY_INSECURE only makes sense with an https:// UPSTREAM_PROXY")
	}

	if c.RateLimit, err = float(getenv("M3_RATE_LIMIT"), c.RateLimit); err != nil {
		return nil, fmt.Errorf("M3_RATE_LIMIT: %w", err)
	}
	if c.RateBurst, err = integer(getenv("M3_RATE_BURST"), c.RateBurst, 1, 10000); err != nil {
		return nil, fmt.Errorf("M3_RATE_BURST: %w", err)
	}
	if c.MaxInFlight, err = integer(getenv("M3_MAX_IN_FLIGHT"), c.MaxInFlight, 1, 100000); err != nil {
		return nil, fmt.Errorf("M3_MAX_IN_FLIGHT: %w", err)
	}
	if c.ImagesPerRun, err = integer(getenv("M3_IMAGES_PER_RUN"), c.ImagesPerRun, 0, 1000000); err != nil {
		return nil, fmt.Errorf("M3_IMAGES_PER_RUN: %w", err)
	}
	if c.ImageMaxWidth, err = integer(getenv("M3_IMAGE_MAX_WIDTH"), c.ImageMaxWidth, 0, 4096); err != nil {
		return nil, fmt.Errorf("M3_IMAGE_MAX_WIDTH: %w", err)
	}
	if c.BackupsKept, err = integer(getenv("M3_BACKUPS_KEPT"), c.BackupsKept, 0, 1000); err != nil {
		return nil, fmt.Errorf("M3_BACKUPS_KEPT: %w", err)
	}
	if c.MaxRequests, err = integer(getenv("M3_MAX_REQUESTS"), c.MaxRequests, 10, 100000); err != nil {
		return nil, fmt.Errorf("M3_MAX_REQUESTS: %w", err)
	}
	if c.RecheckPerRun, err = integer(getenv("M3_RECHECK_PER_RUN"), c.RecheckPerRun, 0, 500); err != nil {
		return nil, fmt.Errorf("M3_RECHECK_PER_RUN: %w", err)
	}
	if c.PublicAPI, err = boolDefault(getenv("M3_PUBLIC_API"), true); err != nil {
		return nil, fmt.Errorf("M3_PUBLIC_API: %w", err)
	}
	if c.PublicExport, err = boolDefault(getenv("M3_PUBLIC_EXPORT"), true); err != nil {
		return nil, fmt.Errorf("M3_PUBLIC_EXPORT: %w", err)
	}
	if c.RefreshPerRun, err = integer(getenv("M3_REFRESH_PER_RUN"), c.RefreshPerRun, 0, 500); err != nil {
		return nil, fmt.Errorf("M3_REFRESH_PER_RUN: %w", err)
	}
	if v := getenv("M3_REFRESH_DELAY_MS"); v != "" {
		ms, err := integer(v, 0, 500, 600000)
		if err != nil {
			return nil, fmt.Errorf("M3_REFRESH_DELAY_MS: %w", err)
		}
		c.RefreshDelay = time.Duration(ms) * time.Millisecond
	}
	if v := getenv("M3_PUBLIC_URL"); strings.TrimSpace(v) != "" {
		u, err := ParsePublicURL(v)
		if err != nil {
			return nil, fmt.Errorf("M3_PUBLIC_URL: %w", err)
		}
		c.PublicURL = u
	}
	if v := getenv("M3_LOCK_WAIT_SECONDS"); v != "" {
		sec, err := integer(v, 0, 0, 3600)
		if err != nil {
			return nil, fmt.Errorf("M3_LOCK_WAIT_SECONDS: %w", err)
		}
		c.LockWait = time.Duration(sec) * time.Second
	}
	if v := getenv("M3_RECHECK_DELAY_MS"); v != "" {
		ms, err := integer(v, 0, 500, 600000)
		if err != nil {
			return nil, fmt.Errorf("M3_RECHECK_DELAY_MS: %w", err)
		}
		c.RecheckDelay = time.Duration(ms) * time.Millisecond
	}
	if v := getenv("M3_REQUEST_DELAY_MS"); v != "" {
		ms, err := integer(v, 0, 0, 60000)
		if err != nil {
			return nil, fmt.Errorf("M3_REQUEST_DELAY_MS: %w", err)
		}
		c.RequestDelay = time.Duration(ms) * time.Millisecond
	}

	return c, nil
}

// ParseProxyURL validates a proxy URL: http:// or https://, a host, optional user:password, nothing else.
func ParseProxyURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return nil, fmt.Errorf("not a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("scheme must be http or https (got %q)", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("missing host")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("must be only scheme://[user:password@]host[:port]")
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("invalid port")
		}
	}
	return u, nil
}

// ParsePublicURL validates the public address of the site and returns it in the form
// scheme://host[:port] without a trailing slash. Only an origin is accepted: no user info, path,
// query or fragment, so links built from it cannot lead anywhere else.
func ParsePublicURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || strings.ContainsAny(raw, " \t\r\n\\") {
		return "", fmt.Errorf("not a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("scheme must be http or https (got %q)", u.Scheme)
	}
	if u.Hostname() == "" || u.User != nil {
		return "", fmt.Errorf("must be scheme://host[:port], without user info")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" || strings.HasSuffix(raw, "?") || strings.HasSuffix(raw, "#") {
		return "", fmt.Errorf("must be only scheme://host[:port] (no path, query or fragment)")
	}
	if p := u.Port(); p != "" {
		n, err := strconv.Atoi(p)
		if err != nil || n < 1 || n > 65535 {
			return "", fmt.Errorf("invalid port")
		}
	}
	return u.Scheme + "://" + strings.ToLower(u.Host), nil
}

// RedactedProxy returns scheme://host of the proxy, without any credentials, for logs.
func RedactedProxy(u *url.URL) string {
	if u == nil {
		return "none"
	}
	return u.Scheme + "://" + u.Host
}

func str(getenv func(string) string, key, def string) string {
	if v := strings.TrimSpace(getenv(key)); v != "" {
		return v
	}
	return def
}

func boolean(v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "", "0", "false", "no", "off":
		return false, nil
	case "1", "true", "yes", "on":
		return true, nil
	}
	return false, fmt.Errorf("expected 0/1/true/false")
}

func boolDefault(v string, def bool) (bool, error) {
	if strings.TrimSpace(v) == "" {
		return def, nil
	}
	return boolean(v)
}

func integer(v string, def, min, max int) (int, error) {
	if strings.TrimSpace(v) == "" {
		return def, nil
	}
	n, err := strconv.Atoi(strings.TrimSpace(v))
	if err != nil || n < min || n > max {
		return 0, fmt.Errorf("expected an integer between %d and %d", min, max)
	}
	return n, nil
}

func float(v string, def float64) (float64, error) {
	if strings.TrimSpace(v) == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || f <= 0 || f > 100000 {
		return 0, fmt.Errorf("expected a positive number")
	}
	return f, nil
}

func parsePrefixes(v string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, part := range strings.Split(v, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if p, err := netip.ParsePrefix(part); err == nil {
			out = append(out, p.Masked())
			continue
		}
		a, err := netip.ParseAddr(part)
		if err != nil {
			return nil, fmt.Errorf("%q is neither an IP nor a CIDR", part)
		}
		out = append(out, netip.PrefixFrom(a, a.BitLen()))
	}
	return out, nil
}
