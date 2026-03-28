package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/iss/evidence-store/internal/api"
	"github.com/iss/evidence-store/internal/config"
	"github.com/iss/evidence-store/internal/store"
)

type Server struct {
	httpServer *http.Server
	pool       *pgxpool.Pool
}

func New(cfg *config.Config, pool *pgxpool.Pool) *Server {
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

	evidenceStore := store.NewEvidenceStore(pool)
	inheritanceStore := store.NewInheritanceStore(pool)

	evidenceAPI := api.NewEvidenceHandler(evidenceStore, inheritanceStore, cfg)
	inheritanceAPI := api.NewInheritanceHandler(inheritanceStore)

	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/evidence", evidenceAPI.Create)
		r.Post("/evidence/batch", evidenceAPI.CreateBatch)
		r.Get("/evidence", evidenceAPI.List)
		r.Get("/evidence/{id}", evidenceAPI.Get)

		r.Post("/inheritance", inheritanceAPI.Create)
		r.Get("/inheritance", inheritanceAPI.List)
	})

	return &Server{
		httpServer: &http.Server{
			Addr:         cfg.ListenAddr,
			Handler:      r,
			ReadTimeout:  10 * time.Second,
			WriteTimeout: 30 * time.Second,
			IdleTimeout:  60 * time.Second,
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
