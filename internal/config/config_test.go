package config

import (
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestDefaults(t *testing.T) {
	c, err := FromEnv(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.DataDir != "/data" || c.Listen != ":8080" || c.UpstreamProxy != nil || c.UpstreamProxyInsecure {
		t.Fatalf("%+v", c)
	}
	if c.UpstreamUserAgent != DefaultUserAgent || !strings.HasPrefix(c.UpstreamUserAgent, "Mozilla/5.0 ") || !strings.Contains(c.UpstreamUserAgent, "Chrome/") {
		t.Fatalf("the default User-Agent must be a regular browser one: %q", c.UpstreamUserAgent)
	}
	if strings.Contains(strings.ToLower(c.UpstreamUserAgent), "m3-keresheto") || strings.Contains(strings.ToLower(c.UpstreamUserAgent), "bot") {
		t.Fatal("the User-Agent must not announce this program")
	}
	if c.ImageMaxWidth != 480 || c.ImagesPerRun != 300 || c.RequestDelay != 250*time.Millisecond {
		t.Fatalf("%+v", c)
	}
}

func TestUserAgentOverride(t *testing.T) {
	c, _ := FromEnv(env(map[string]string{"UPSTREAM_USER_AGENT": "Mozilla/5.0 (X11; Linux x86_64) Firefox/200.0"}))
	if c.UpstreamUserAgent != "Mozilla/5.0 (X11; Linux x86_64) Firefox/200.0" {
		t.Fatal(c.UpstreamUserAgent)
	}
}

func TestProxySettings(t *testing.T) {
	good := map[string]string{
		"http://proxy.example:3128":        "http://proxy.example:3128",
		"https://proxy.example:8443":       "https://proxy.example:8443",
		"http://proxy.example":             "http://proxy.example",
		"http://alice:s3cret@10.0.0.5:80":  "http://10.0.0.5:80",
		"https://alice:s3cret@[::1]:8443/": "https://[::1]:8443",
		" http://proxy.example:3128 ":      "http://proxy.example:3128",
	}
	for in, wantLabel := range good {
		c, err := FromEnv(env(map[string]string{"UPSTREAM_PROXY": in}))
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if got := RedactedProxy(c.UpstreamProxy); got != wantLabel {
			t.Errorf("%q: label %q, want %q", in, got, wantLabel)
		}
		if l := RedactedProxy(c.UpstreamProxy); strings.Contains(l, "alice") || strings.Contains(l, "s3cret") {
			t.Errorf("credentials in the log label: %s", l)
		}
	}

	bad := []string{
		"socks5://proxy:1080", "ftp://proxy", "proxy.example:3128", "//proxy.example", "http://", "http://:3128",
		"http://proxy.example:99999", "http://proxy.example:abc", "http://proxy.example/path", "http://proxy.example?x=1",
		"http://proxy.example#frag", "file:///etc/passwd", "javascript:alert(1)",
	}
	for _, in := range bad {
		if _, err := FromEnv(env(map[string]string{"UPSTREAM_PROXY": in})); err == nil {
			t.Errorf("%q accepted", in)
		}
	}
}

func TestErrorsNeverEchoTheProxyPassword(t *testing.T) {
	_, err := FromEnv(env(map[string]string{"UPSTREAM_PROXY": "socks5://alice:TOPSECRET@proxy:1080"}))
	if err == nil || strings.Contains(err.Error(), "TOPSECRET") || strings.Contains(err.Error(), "alice") {
		t.Fatalf("error leaks the credentials: %v", err)
	}
}

func TestInsecureFlagOnlyForHTTPSProxies(t *testing.T) {
	if _, err := FromEnv(env(map[string]string{"UPSTREAM_PROXY_INSECURE": "1"})); err == nil {
		t.Error("insecure without a proxy accepted")
	}
	if _, err := FromEnv(env(map[string]string{"UPSTREAM_PROXY": "http://p:3128", "UPSTREAM_PROXY_INSECURE": "1"})); err == nil {
		t.Error("insecure with an http proxy accepted (nothing to verify)")
	}
	c, err := FromEnv(env(map[string]string{"UPSTREAM_PROXY": "https://p:8443", "UPSTREAM_PROXY_INSECURE": "true"}))
	if err != nil || !c.UpstreamProxyInsecure {
		t.Fatalf("%v", err)
	}
	if _, err := FromEnv(env(map[string]string{"UPSTREAM_PROXY": "https://p:8443", "UPSTREAM_PROXY_INSECURE": "maybe"})); err == nil {
		t.Error("garbage boolean accepted")
	}
}

func TestNumbersAndNetworks(t *testing.T) {
	for k, v := range map[string]string{
		"M3_RATE_LIMIT": "0", "M3_RATE_BURST": "0", "M3_MAX_IN_FLIGHT": "-3", "M3_IMAGES_PER_RUN": "x",
		"M3_IMAGE_MAX_WIDTH": "99999", "M3_BACKUPS_KEPT": "-1", "M3_REQUEST_DELAY_MS": "999999", "M3_TRUSTED_PROXIES": "10.0.0.0/33",
	} {
		if _, err := FromEnv(env(map[string]string{k: v})); err == nil {
			t.Errorf("%s=%s accepted", k, v)
		}
	}
	c, err := FromEnv(env(map[string]string{"M3_TRUSTED_PROXIES": "10.0.0.0/8, 192.168.1.5 ,fd00::/8", "M3_IMAGES_PER_RUN": "0", "M3_REQUEST_DELAY_MS": "0"}))
	if err != nil || len(c.TrustedProxies) != 3 || c.ImagesPerRun != 0 || c.RequestDelay != 0 {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestRequestBudgetAndRecheckDefaultsAreGentle(t *testing.T) {
	c, err := FromEnv(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if c.RecheckPerRun != 0 {
		t.Fatalf("the recheck must be OFF by default, got %d", c.RecheckPerRun)
	}
	if c.MaxRequests != 2000 || c.RecheckDelay != 3*time.Second {
		t.Fatalf("%d %s", c.MaxRequests, c.RecheckDelay)
	}
	c, err = FromEnv(env(map[string]string{"M3_RECHECK_PER_RUN": "30", "M3_MAX_REQUESTS": "500", "M3_RECHECK_DELAY_MS": "5000"}))
	if err != nil || c.RecheckPerRun != 30 || c.MaxRequests != 500 || c.RecheckDelay != 5*time.Second {
		t.Fatalf("%+v %v", c, err)
	}
	for k, v := range map[string]string{
		"M3_RECHECK_PER_RUN": "501", "M3_MAX_REQUESTS": "9", "M3_RECHECK_DELAY_MS": "100", "M3_RECHECK_DELAY_MS ": "x",
	} {
		if _, err := FromEnv(env(map[string]string{strings.TrimSpace(k): v})); err == nil {
			t.Errorf("%s=%s accepted: the limits must keep the program from hammering the upstream", k, v)
		}
	}
}

func TestLockWait(t *testing.T) {
	c, err := FromEnv(env(nil))
	if err != nil || c.LockWait != 10*time.Minute {
		t.Fatalf("default: %v %v", c.LockWait, err)
	}
	for in, want := range map[string]time.Duration{"0": 0, "30": 30 * time.Second, "3600": time.Hour} {
		c, err := FromEnv(env(map[string]string{"M3_LOCK_WAIT_SECONDS": in}))
		if err != nil || c.LockWait != want {
			t.Errorf("M3_LOCK_WAIT_SECONDS=%s: %v %v", in, c.LockWait, err)
		}
	}
	for _, bad := range []string{"-1", "3601", "x", "1.5"} {
		if _, err := FromEnv(env(map[string]string{"M3_LOCK_WAIT_SECONDS": bad})); err == nil {
			t.Errorf("M3_LOCK_WAIT_SECONDS=%s accepted", bad)
		}
	}
}

func TestPublicURLMustBeAnOrigin(t *testing.T) {
	c, err := FromEnv(env(nil))
	if err != nil || c.PublicURL != "" {
		t.Fatalf("default must be empty (the site is then private to search engines): %q %v", c.PublicURL, err)
	}
	for in, want := range map[string]string{
		"https://m3.example.hu":      "https://m3.example.hu",
		"https://M3.Example.HU/":     "https://m3.example.hu",
		"  https://m3.example.hu  ":  "https://m3.example.hu",
		"http://localhost:8080":      "http://localhost:8080",
		"https://sub.example.hu:443": "https://sub.example.hu:443",
	} {
		c, err := FromEnv(env(map[string]string{"M3_PUBLIC_URL": in}))
		if err != nil || c.PublicURL != want {
			t.Errorf("%q -> %q, %v (want %q)", in, c.PublicURL, err, want)
		}
	}
	for _, bad := range []string{
		"m3.example.hu", "ftp://m3.example.hu", "javascript:alert(1)", "https://", "https:///x", "//m3.example.hu",
		"https://user:pw@m3.example.hu", "https://m3.example.hu/path", "https://m3.example.hu/?q=1", "https://m3.example.hu#frag",
		"https://m3.example.hu?", "https://m3.example.hu:99999", "https://m3.example.hu:0", "https://m3.example hu", "https://m3.example.hu\\x",
	} {
		if c, err := FromEnv(env(map[string]string{"M3_PUBLIC_URL": bad})); err == nil {
			t.Errorf("%q accepted as %q: only an origin may be used to build absolute links", bad, c.PublicURL)
		}
	}
}
