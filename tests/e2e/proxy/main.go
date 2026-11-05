// Command e2e-proxy is a minimal HTTP/HTTPS CONNECT proxy for the end-to-end tests.
// It is NOT part of the product: it exists so the tests can prove that the ingest sends
// all its traffic through a configured proxy (and nothing leaves on its own).
//
//	e2e-proxy -listen :3128                       plain http proxy
//	e2e-proxy -listen :8443 -tls                  https proxy with a self-signed certificate
//	e2e-proxy -listen :3128 -auth user:password   require Basic proxy authentication
//
// Every CONNECT is logged as a JSON line on stdout.
package main

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"flag"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"os"
	"time"
)

func main() {
	listen := flag.String("listen", ":3128", "listen address")
	useTLS := flag.Bool("tls", false, "serve TLS with a generated self-signed certificate")
	auth := flag.String("auth", "", "require user:password")
	flag.Parse()

	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Fatal(err)
	}
	if *useTLS {
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{selfSigned()}, MinVersion: tls.VersionTLS12})
	}
	want := ""
	if *auth != "" {
		want = "Basic " + base64.StdEncoding.EncodeToString([]byte(*auth))
	}
	enc := json.NewEncoder(os.Stdout)
	for {
		c, err := ln.Accept()
		if err != nil {
			log.Fatal(err)
		}
		go handle(c, want, enc)
	}
}

func handle(c net.Conn, want string, enc *json.Encoder) {
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(30 * time.Second))
	br := bufio.NewReader(c)
	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	entry := map[string]any{"time": time.Now().UTC().Format(time.RFC3339), "method": req.Method, "target": req.Host, "ua": req.UserAgent()}
	if req.Method != http.MethodConnect {
		entry["result"] = "refused (not CONNECT)"
		_ = enc.Encode(entry)
		_, _ = io.WriteString(c, "HTTP/1.1 405 Method Not Allowed\r\nContent-Length: 0\r\n\r\n")
		return
	}
	if want != "" && req.Header.Get("Proxy-Authorization") != want {
		entry["result"] = "407"
		_ = enc.Encode(entry)
		_, _ = io.WriteString(c, "HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"e2e\"\r\nContent-Length: 0\r\n\r\n")
		return
	}
	dst, err := net.DialTimeout("tcp", req.Host, 15*time.Second)
	if err != nil {
		entry["result"] = "502 " + err.Error()
		_ = enc.Encode(entry)
		_, _ = io.WriteString(c, "HTTP/1.1 502 Bad Gateway\r\nContent-Length: 0\r\n\r\n")
		return
	}
	defer dst.Close()
	entry["result"] = "200"
	_ = enc.Encode(entry)
	_, _ = io.WriteString(c, "HTTP/1.1 200 Connection Established\r\n\r\n")
	_ = c.SetDeadline(time.Time{})
	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(dst, br); done <- struct{}{} }()
	go func() { _, _ = io.Copy(c, dst); done <- struct{}{} }()
	<-done
}

func selfSigned() tls.Certificate {
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "e2e proxy"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		DNSNames:     []string{"m3proxy", "localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		log.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}
