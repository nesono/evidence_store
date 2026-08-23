package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nesono/evidence-store/internal/api"
	"github.com/nesono/evidence-store/internal/auth"
	"github.com/nesono/evidence-store/internal/blob"
	"github.com/nesono/evidence-store/internal/config"
	"github.com/nesono/evidence-store/internal/ratelimit"
	"github.com/nesono/evidence-store/internal/store"
	"github.com/nesono/evidence-store/web"
)

type Server struct {
	httpServer *http.Server
	pool       *pgxpool.Pool
}

func New(cfg *config.Config, pool *pgxpool.Pool, blobs blob.Store) *Server {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(30 * time.Second))

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if err := pool.Ping(r.Context()); err != nil {
			http.Error(w, "database unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	evidenceStore := store.NewEvidenceStoreWithCache(pool, cfg.AnalyticsCacheTTL)
	inheritanceStore := store.NewInheritanceStore(pool)

	evidenceAPI := api.NewEvidenceHandler(evidenceStore, inheritanceStore, cfg)
	inheritanceAPI := api.NewInheritanceHandler(inheritanceStore)
	analyticsAPI := api.NewAnalyticsHandler(evidenceStore, cfg)
	blobAPI := api.NewBlobHandler(blobs, cfg.Blob.MaxBytes)

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(auth.Middleware(cfg.APIKeys))
		r.Use(ratelimit.Middleware(cfg.RateLimit))

		r.Post("/evidence", evidenceAPI.Create)
		r.Post("/evidence/batch", evidenceAPI.CreateBatch)
		r.Get("/evidence", evidenceAPI.List)
		r.Get("/evidence/distinct", evidenceAPI.Distinct)
		r.Get("/evidence/{id}", evidenceAPI.Get)

		r.Post("/inheritance", inheritanceAPI.Create)
		r.Get("/inheritance", inheritanceAPI.List)

		r.Get("/analytics/summary", analyticsAPI.Summary)
		r.Get("/analytics/tests", analyticsAPI.Tests)
		r.Get("/analytics/clusters", analyticsAPI.Clusters)

		// Uploading is a write, reading is a read, so the API-key roles and the
		// rate limiter's buckets already apply the right rules to both.
		r.Post("/blobs", blobAPI.Upload)
		r.Get("/blobs/{ref}", blobAPI.Get)
	})

	r.Handle("/*", web.StaticHandler())

	return &Server{
		httpServer: &http.Server{
			Addr:    cfg.ListenAddr,
			Handler: r,
			// The body may now be a few megabytes of screenshot from a tester on
			// a phone tether, which the previous 10s read timeout would cut off
			// mid-upload. Headers keep the short deadline, so a connection that
			// dawdles before saying anything is still dropped promptly.
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       60 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
		pool: pool,
	}
}

func (s *Server) Handler() http.Handler {
	return s.httpServer.Handler
}

func (s *Server) Start() error {
	slog.Info("server starting", "addr", s.httpServer.Addr)
	return s.httpServer.ListenAndServe()
}

func (s *Server) Shutdown(ctx context.Context) error {
	slog.Info("server shutting down")
	return s.httpServer.Shutdown(ctx)
}
