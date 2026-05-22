// Package httpserver provides a chi-based HTTP server with project-standard
// middleware. Each bounded context's cmd/<ctx>-svc/main.go calls New() and
// mounts its own routes.
package httpserver

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/cors"
)

type Server struct {
	Router *chi.Mux
	http   *http.Server
	log    *slog.Logger

	// Health hooks — set by SetHealth before Run. The /healthz handler
	// reads them on each request, so they can be wired post-construction.
	healthName string
	healthPing func(context.Context) error
}

// SetHealth wires a DB ping (or any other readiness probe) into /healthz.
// Service binaries call this from main after constructing their pool.
// The handler returns 503 with the error message when ping fails.
func (s *Server) SetHealth(name string, ping func(context.Context) error) {
	s.healthName = name
	s.healthPing = ping
}

type Config struct {
	Port         int
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	IdleTimeout  time.Duration
	CORSOrigins  []string
}

func DefaultConfig(port int) Config {
	return Config{
		Port:         port,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
		CORSOrigins:  defaultCORSOrigins(),
	}
}

// defaultCORSOrigins reads the `CORS_ORIGINS` env (comma-separated) so the
// allowlist can be extended for preview/staging without code edits. Falls
// back to the local dev origins when unset:
//   - http://localhost:3000               — Next.js dev port (web dashboard)
//   - http://127.0.0.1:9100/9101/9102     — Flutter web `flutter run -d web-server`
//     for customer_app / sales_app / tech_app respectively
//   - http://localhost:9100/9101/9102     — same ports via the `localhost` alias
//
// Production overrides this via CORS_ORIGINS to the real frontend hostnames.
// chi/cors does exact-string matching, so localhost vs 127.0.0.1 are
// treated as separate origins — both must be listed if both are reachable.
func defaultCORSOrigins() []string {
	raw := strings.TrimSpace(os.Getenv("CORS_ORIGINS"))
	if raw == "" {
		return []string{
			"http://localhost:3000",
			"http://127.0.0.1:9100", "http://localhost:9100", // customer_app
			"http://127.0.0.1:9101", "http://localhost:9101", // sales_app
			"http://127.0.0.1:9102", "http://localhost:9102", // tech_app
		}
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	if len(out) == 0 {
		return []string{"http://localhost:3000"}
	}
	return out
}

// New constructs a Server with the standard middleware chain mounted.
func New(cfg Config, log *slog.Logger) *Server {
	r := chi.NewRouter()

	r.Use(chimw.RequestID)
	r.Use(chimw.RealIP)
	r.Use(RequestLogger(log))
	r.Use(chimw.Recoverer)
	r.Use(chimw.Timeout(cfg.WriteTimeout))
	r.Use(cors.Handler(cors.Options{
		AllowedOrigins:   cfg.CORSOrigins,
		AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		// Save-Data is sent automatically by Chrome's mobile emulation
		// and any client running with the data-saver preference. The
		// preflight rejects it if it's not on the allow-list, which
		// surfaces in the SPA as a generic "CORS error". Add it (and
		// the other common client-hint headers that mobile emulation
		// turns on) so devtools mobile mode doesn't break the build.
		AllowedHeaders: []string{
			"Accept", "Authorization", "Content-Type", "X-Request-ID",
			"Save-Data", "DPR", "Viewport-Width", "Width",
			"Downlink", "ECT", "RTT",
		},
		ExposedHeaders:   []string{"X-Request-ID"},
		AllowCredentials: true,
		MaxAge:           300,
	}))

	s := &Server{
		Router: r,
		http: &http.Server{
			Addr:         fmt.Sprintf(":%d", cfg.Port),
			ReadTimeout:  cfg.ReadTimeout,
			WriteTimeout: cfg.WriteTimeout,
			IdleTimeout:  cfg.IdleTimeout,
		},
		log: log,
	}

	// /healthz reads s.healthName + s.healthPing per request so SetHealth
	// can wire them at any time after New(). When ping is nil we just
	// respond ok — binaries without DB still get a basic liveness probe.
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		name := s.healthName
		if name == "" {
			name = "service"
		}
		w.Header().Set("content-type", "application/json")
		if s.healthPing != nil {
			ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
			defer cancel()
			if err := s.healthPing(ctx); err != nil {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = fmt.Fprintf(w, `{"service":%q,"status":"down","error":%q}`, name, err.Error())
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		_, _ = fmt.Fprintf(w, `{"service":%q,"status":"ok"}`, name)
	})

	return s
}

// Run starts the server and blocks until ctx is cancelled.
// On cancellation it performs a graceful shutdown.
func (s *Server) Run(ctx context.Context) error {
	s.http.Handler = s.Router

	errCh := make(chan error, 1)
	go func() {
		s.log.Info("http server listening", "addr", s.http.Addr)
		if err := s.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		s.log.Info("shutting down http server")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return s.http.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}
