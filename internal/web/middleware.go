package web

import (
	"net/http"
	"runtime/debug"
	"time"
)

// statusWriter records the status and size of a response for the access log.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController and io.ReaderFrom find the real writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// recoverer turns a panic into a plain 500 without leaking anything.
func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				s.o.Log.Error("panic in handler", "panic", v, "stack", string(debug.Stack()))
				http.Error(w, "Belső hiba.", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// secure sets the security headers on EVERY response (also 404, 405, 429, 5xx), before any
// handler runs, and refuses over-long URLs.
func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", csp)
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), interest-cohort=()")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Strict-Transport-Security", "max-age=31536000; includeSubDomains")
		h.Set("X-Frame-Options", "DENY")

		if len(r.RequestURI) > maxURLLength {
			http.Error(w, "A kérés címe túl hosszú.", http.StatusRequestURITooLong)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// guard limits the number of requests handled at once and the rate per visitor.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" { // the container health check must always get through
			next.ServeHTTP(w, r)
			return
		}
		select {
		case s.inflight <- struct{}{}:
			defer func() { <-s.inflight }()
		default:
			w.Header().Set("Retry-After", "5")
			http.Error(w, "A szerver jelenleg túlterhelt.", http.StatusServiceUnavailable)
			return
		}
		if !s.limiter.allow(clientIP(r, s.o.TrustedProxies)) {
			w.Header().Set("Retry-After", "2")
			http.Error(w, "Túl sok kérés, kérlek lassíts.", http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// access writes one structured log line per request. The query string (the visitor's search)
// is not logged and the address is a daily rotating pseudonym.
func (s *Server) access(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)
		s.o.Log.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"bytes", sw.bytes,
			"ms", time.Since(start).Milliseconds(),
			"client", s.anon.name(clientIP(r, s.o.TrustedProxies)),
		)
	})
}
