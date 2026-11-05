package upstream

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/py-snake/m3-keresheto-archivum/internal/fakeupstream"
)

// testProxy is a minimal CONNECT proxy.
type testProxy struct {
	addr     string
	connects atomic.Int32
	mu       sync.Mutex
	targets  []string
	agents   []string
	status   int    // when non-zero, answer CONNECT with this status instead of tunnelling
	wantAuth string // when set, require this Proxy-Authorization header
}

func newTestProxy(t *testing.T, tlsConf *tls.Config) *testProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	if tlsConf != nil {
		ln = tls.NewListener(ln, tlsConf)
	}
	p := &testProxy{addr: ln.Addr().String()}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go p.handle(c)
		}
	}()
	return p
}

func (p *testProxy) handle(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil || req.Method != http.MethodConnect {
		return
	}
	p.connects.Add(1)
	p.mu.Lock()
	p.targets = append(p.targets, req.Host)
	p.agents = append(p.agents, req.UserAgent())
	status, want := p.status, p.wantAuth
	p.mu.Unlock()

	if want != "" && req.Header.Get("Proxy-Authorization") != want {
		_, _ = io.WriteString(c, "HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n")
		return
	}
	if status != 0 {
		_, _ = io.WriteString(c, "HTTP/1.1 "+strconv.Itoa(status)+" "+http.StatusText(status)+"\r\nContent-Length: 0\r\n\r\n")
		return
	}
	dst, err := net.Dial("tcp", req.Host)
	if err != nil {
		_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer dst.Close()
	_, _ = io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n")
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(dst, br); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, dst); done <- struct{}{} }()
	<-done
}

// selfSigned makes a throw-away certificate for 127.0.0.1 (a "self-signed proxy").
func selfSigned(t *testing.T) *tls.Config {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test proxy"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}

func proxyURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func clientVia(t *testing.T, fake *fakeupstream.Server, proxy *url.URL, insecure bool, trustFake bool) *Client {
	t.Helper()
	o := Options{BaseURL: fake.URL, UserAgent: "test-agent", Proxy: proxy, ProxyInsecure: insecure, Timeout: 10 * time.Second}
	if trustFake {
		o.RootCAs = fake.CAs
	}
	c, err := NewClient(o)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestHTTPProxy(t *testing.T) {
	fake := fakeupstream.New(t)
	p := newTestProxy(t, nil)
	c := clientVia(t, fake, proxyURL(t, "http://"+p.addr), false, true)

	if _, err := c.CollectionIDs(context.Background()); err != nil {
		t.Fatalf("request through the http proxy failed: %v", err)
	}
	if p.connects.Load() != 1 {
		t.Fatalf("expected exactly 1 CONNECT through the proxy, got %d", p.connects.Load())
	}
	// the destination is sent as host:port; the request itself went through the tunnel
	if got := p.targets[0]; got != strings.TrimPrefix(fake.URL, "https://") {
		t.Fatalf("CONNECT target = %q", got)
	}
	if p.agents[0] != "test-agent" {
		t.Fatalf("CONNECT User-Agent = %q, want the configured one", p.agents[0])
	}
	if fake.UserAgents()["test-agent"] != 1 {
		t.Fatalf("the upstream did not see the configured User-Agent: %v", fake.UserAgents())
	}
}

func TestHTTPSProxySelfSigned(t *testing.T) {
	fake := fakeupstream.New(t)
	p := newTestProxy(t, selfSigned(t))

	// secure by default: a self-signed proxy certificate is refused, and there is no fallback
	c := clientVia(t, fake, proxyURL(t, "https://"+p.addr), false, true)
	if _, err := c.CollectionIDs(context.Background()); err == nil {
		t.Fatal("self-signed https proxy accepted without the insecure flag")
	}
	if p.connects.Load() != 0 || fake.Count("/m3/get-open") != 0 {
		t.Fatal("traffic must not flow when the proxy certificate is rejected")
	}

	// with UPSTREAM_PROXY_INSECURE the proxy certificate is not verified
	c = clientVia(t, fake, proxyURL(t, "https://"+p.addr), true, true)
	if _, err := c.CollectionIDs(context.Background()); err != nil {
		t.Fatalf("insecure https proxy failed: %v", err)
	}
	if p.connects.Load() != 1 {
		t.Fatalf("CONNECT count = %d", p.connects.Load())
	}
}

func TestInsecureProxyDoesNotWeakenUpstreamVerification(t *testing.T) {
	fake := fakeupstream.New(t)
	p := newTestProxy(t, selfSigned(t))

	// proxy certificate unverified, but the upstream's certificate is NOT trusted by the client
	c := clientVia(t, fake, proxyURL(t, "https://"+p.addr), true, false)
	_, err := c.CollectionIDs(context.Background())
	if err == nil {
		t.Fatal("upstream certificate must still be verified through an insecure proxy")
	}
	if !strings.Contains(err.Error(), "certificate") && !strings.Contains(err.Error(), "x509") {
		t.Fatalf("expected a certificate error, got: %v", err)
	}
	if fake.Count("/m3/get-open") != 0 {
		t.Fatal("no request may reach the upstream when its certificate is untrusted")
	}
}

func TestProxyAuthAndNoPasswordLeak(t *testing.T) {
	fake := fakeupstream.New(t)
	p := newTestProxy(t, nil)
	p.wantAuth = "Basic " + base64.StdEncoding.EncodeToString([]byte("alice:s3cret"))

	c := clientVia(t, fake, proxyURL(t, "http://alice:s3cret@"+p.addr), false, true)
	if _, err := c.CollectionIDs(context.Background()); err != nil {
		t.Fatalf("authenticated proxy request failed: %v", err)
	}

	bad := clientVia(t, fake, proxyURL(t, "http://alice:WRONGPASS@"+p.addr), false, true)
	_, err := bad.CollectionIDs(context.Background())
	if err == nil {
		t.Fatal("wrong proxy credentials accepted")
	}
	if strings.Contains(err.Error(), "WRONGPASS") || strings.Contains(err.Error(), "alice:") {
		t.Fatalf("proxy password leaked into the error: %v", err)
	}
	if !strings.Contains(err.Error(), "407") {
		t.Fatalf("expected the 407 status in the error, got: %v", err)
	}
}

func TestProxyRefusal(t *testing.T) {
	fake := fakeupstream.New(t)
	p := newTestProxy(t, nil)
	p.status = http.StatusForbidden
	c := clientVia(t, fake, proxyURL(t, "http://"+p.addr), false, true)
	if _, err := c.CollectionIDs(context.Background()); err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("expected a refusal error, got %v", err)
	}
}

func TestNoFallbackToDirectWhenProxyIsDown(t *testing.T) {
	fake := fakeupstream.New(t)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := ln.Addr().String()
	_ = ln.Close() // nothing listens there any more

	c := clientVia(t, fake, proxyURL(t, "http://"+dead), false, true)
	if _, err := c.CollectionIDs(context.Background()); err == nil {
		t.Fatal("request succeeded although the proxy is down: traffic went out directly")
	}
	if len(fake.Requests) != 0 {
		t.Fatal("the upstream saw a direct connection: the proxy was bypassed")
	}
}

func TestEnvironmentProxyVariablesAreIgnored(t *testing.T) {
	fake := fakeupstream.New(t)
	p := newTestProxy(t, nil)
	t.Setenv("HTTPS_PROXY", "http://"+p.addr)
	t.Setenv("https_proxy", "http://"+p.addr)
	t.Setenv("NO_PROXY", "")

	c := clientVia(t, fake, nil, false, true) // direct on purpose
	if _, err := c.CollectionIDs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if p.connects.Load() != 0 {
		t.Fatal("HTTPS_PROXY from the environment must not be used implicitly")
	}
}

func TestProxyBytesAfterConnectAreKept(t *testing.T) {
	// a server that speaks first right after the 200 must not lose its first bytes
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		_, _ = http.ReadRequest(bufio.NewReader(c))
		_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\n\r\nHELLO")
		time.Sleep(200 * time.Millisecond)
	}()
	d := newProxyDialer(proxyURL(t, "http://"+ln.Addr().String()), false, "ua")
	conn, err := d.DialContext(context.Background(), "tcp", "example.invalid:443")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	buf := make([]byte, 5)
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := io.ReadFull(conn, buf); err != nil || string(buf) != "HELLO" {
		t.Fatalf("lost bytes after CONNECT: %q %v", buf, err)
	}
}
