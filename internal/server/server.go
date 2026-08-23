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
	"github.com/nesono/evidence-store/internal/weather"
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

	// An empty endpoint means the operator has switched the lookup off, and the
	// handler is given no provider rather than not being routed: a form whose
	// button does nothing at all is worse than one that says why.
	var weatherProvider weather.Provider
	if cfg.Weather.Endpoint != "" {
		weatherProvider = weather.NewOpenMeteo(cfg.Weather.Endpoint, cfg.Weather.Timeout)
	}
	weatherAPI := api.NewWeatherHandler(weatherProvider)

	// Authentication establishes who is calling; each route then states the
	// permission it needs. Rate limiting stays after authentication so its
	// buckets can eventually key on the principal rather than the token, and so
	// an unauthenticated flood is still rejected before it costs anything.
	//
	// Both key sources are live at once when both are configured, and the env
	// keys come first because checking them costs no round trip. That is the
	// migration path: issue database keys, move pipelines over one at a time,
	// and clear EVIDENCE_API_KEYS when the last one has moved.
	authenticator := auth.Chain{auth.NewStaticKeyAuthenticator(cfg.APIKeys)}
	if cfg.Auth.DB {
		authenticator = append(authenticator,
			auth.NewDBKeyAuthenticator(store.NewPrincipalStore(pool), slog.Default()))
	}

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(auth.Authenticate(authenticator))
		r.Use(ratelimit.Middleware(cfg.RateLimit))

		r.With(auth.Require(auth.PermEvidenceWrite)).Post("/evidence", evidenceAPI.Create)
		r.With(auth.Require(auth.PermEvidenceWrite)).Post("/evidence/batch", evidenceAPI.CreateBatch)
		r.With(auth.Require(auth.PermEvidenceRead)).Get("/evidence", evidenceAPI.List)
		r.With(auth.Require(auth.PermEvidenceRead)).Get("/evidence/distinct", evidenceAPI.Distinct)
		r.With(auth.Require(auth.PermEvidenceRead)).Get("/evidence/{id}", evidenceAPI.Get)

		// Declaring that one branch inherits another's evidence rewrites what
		// the store answers about code nobody tested. DESIGN.md section 8 has
		// always called that an elevated operation; only admin holds it.
		r.With(auth.Require(auth.PermInheritanceWrite)).Post("/inheritance", inheritanceAPI.Create)
		r.With(auth.Require(auth.PermInheritanceRead)).Get("/inheritance", inheritanceAPI.List)

		r.With(auth.Require(auth.PermAnalyticsRead)).Get("/analytics/summary", analyticsAPI.Summary)
		r.With(auth.Require(auth.PermAnalyticsRead)).Get("/analytics/tests", analyticsAPI.Tests)
		r.With(auth.Require(auth.PermAnalyticsRead)).Get("/analytics/clusters", analyticsAPI.Clusters)

		// A read: it writes nothing, and anyone who may read a record may look
		// up the weather that would go on one. There is no weather:read.
		r.With(auth.Require(auth.PermEvidenceRead)).Get("/weather", weatherAPI.Get)

		r.With(auth.Require(auth.PermBlobWrite)).Post("/blobs", blobAPI.Upload)
		r.With(auth.Require(auth.PermBlobRead)).Get("/blobs/{ref}", blobAPI.Get)
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
