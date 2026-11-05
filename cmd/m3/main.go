// Command m3 is the searchable M3 archive: a web server and the ingest jobs that fill it.
//
//	m3 serve                      run the web server (read-only role)
//	m3 ingest daily               fetch today's program and the program guide
//	m3 ingest weekly              fetch the open-archive collections and their series
//	m3 ingest item M3-... [...]   fetch specific programs by id (administrator tool)
//	m3 ingest images              download the pending cover images only
//	m3 healthcheck [url]          exit 0 when the server answers (for container health checks)
//	m3 version
//
// All configuration comes from environment variables, see internal/config.
package main

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/py-snake/m3-keresheto-archivum/internal/config"
	"github.com/py-snake/m3-keresheto-archivum/internal/ingest"
	"github.com/py-snake/m3-keresheto-archivum/internal/upstream"
	"github.com/py-snake/m3-keresheto-archivum/internal/web"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	log := slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if len(args) == 0 {
		usage()
		return 2
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch args[0] {
	case "version":
		fmt.Println(version())
		return 0

	case "healthcheck":
		return healthcheck(ctx, args[1:])

	case "serve":
		cfg, err := config.FromEnv(os.Getenv)
		if err != nil {
			log.Error("configuration error", "err", err)
			return 2
		}
		if err := web.Serve(ctx, cfg, log); err != nil {
			log.Error("server stopped", "err", err)
			return 1
		}
		return 0

	case "ingest":
		return runIngest(ctx, log, args[1:])
	}

	usage()
	return 2
}

func runIngest(ctx context.Context, log *slog.Logger, args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	cfg, err := config.FromEnv(os.Getenv)
	if err != nil {
		log.Error("configuration error", "err", err)
		return 2
	}
	g, err := newIngestor(cfg, log)
	if err != nil {
		log.Error("setup failed", "err", err)
		return 2
	}

	var res ingest.Result
	switch args[0] {
	case "daily":
		res, err = g.Daily(ctx)
	case "weekly":
		res, err = g.Weekly(ctx)
	case "images":
		res, err = g.Images(ctx)
	case "item":
		if len(args) < 2 {
			log.Error("usage: m3 ingest item M3-... [M3-...]")
			return 2
		}
		res, err = g.Items(ctx, args[1:])
	default:
		usage()
		return 2
	}
	if err != nil {
		log.Error("ingest failed", "job", args[0], "err", err, "published", res.Summary.Published)
		return 1
	}
	return 0
}

func newIngestor(cfg *config.Config, log *slog.Logger) (*ingest.Ingestor, error) {
	var pool *x509.CertPool
	if cfg.UpstreamCAFile != "" {
		pem, err := os.ReadFile(cfg.UpstreamCAFile)
		if err != nil {
			return nil, fmt.Errorf("UPSTREAM_CA_FILE: %w", err)
		}
		if pool, err = x509.SystemCertPool(); err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, errors.New("UPSTREAM_CA_FILE: no certificate found")
		}
	}
	up, err := upstream.NewClient(upstream.Options{
		UserAgent:     cfg.UpstreamUserAgent,
		Proxy:         cfg.UpstreamProxy,
		ProxyInsecure: cfg.UpstreamProxyInsecure,
		RootCAs:       pool,
		Delay:         cfg.RequestDelay,
	})
	if err != nil {
		return nil, err
	}
	log.Info("upstream client ready", "proxy", config.RedactedProxy(cfg.UpstreamProxy), "proxy_tls_verification_off", cfg.UpstreamProxyInsecure)
	if cfg.UpstreamProxyInsecure {
		log.Warn("the TLS certificate of the proxy is NOT verified (UPSTREAM_PROXY_INSECURE)")
	}
	return &ingest.Ingestor{
		Dir: cfg.DataDir, Keep: cfg.BackupsKept, Up: up, Log: log,
		ImagesPerRun: cfg.ImagesPerRun, ImageMaxWidth: cfg.ImageMaxWidth,
	}, nil
}

func healthcheck(ctx context.Context, args []string) int {
	url := "http://127.0.0.1:8080/healthz"
	if len(args) > 0 {
		url = args[0]
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 2
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 1
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}

func version() string {
	if bi, ok := debug.ReadBuildInfo(); ok {
		rev, dirty := "", ""
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				rev = s.Value
				if len(rev) > 12 {
					rev = rev[:12]
				}
			case "vcs.modified":
				if s.Value == "true" {
					dirty = "+dirty"
				}
			}
		}
		return "m3 " + bi.GoVersion + " " + rev + dirty
	}
	return "m3 (unknown build)"
}

func usage() {
	fmt.Fprint(os.Stderr, `usage:
  m3 serve
  m3 ingest daily|weekly|images
  m3 ingest item M3-... [M3-...]
  m3 healthcheck [url]
  m3 version
`)
}
