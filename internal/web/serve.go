package web

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/py-snake/m3-keresheto-archivum/internal/config"
	"github.com/py-snake/m3-keresheto-archivum/internal/store"
)

// Serve runs the web server until ctx is cancelled.
func Serve(ctx context.Context, cfg *config.Config, log *slog.Logger) error {
	src := store.NewSource(cfg.DataDir, log)
	defer src.Close()
	if err := src.Refresh(); err != nil {
		// not fatal: the pages answer 503 until a valid database is published
		log.Error("no usable database yet", "err", err)
	}
	go src.Run(ctx)

	s, err := New(OptionsFrom(cfg, src, log))
	if err != nil {
		return err
	}
	defer s.Close()

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       10 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       30 * time.Second,
		MaxHeaderBytes:    8 << 10,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Info("serving", "addr", cfg.Listen, "data", cfg.DataDir)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
