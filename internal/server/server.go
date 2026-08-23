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

// New builds the router. sso is nil unless an identity provider is configured;
// discovering one is network I/O that can fail, so it happens at startup in
// cmd/server rather than here, where there would be nowhere to report it.
func New(cfg *config.Config, pool *pgxpool.Pool, blobs blob.Store, sso *auth.OIDCProvider) *Server {
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

	principalStore := store.NewPrincipalStore(pool)
	sessionStore := store.NewSessionStore(pool)

	evidenceAPI := api.NewEvidenceHandler(evidenceStore, inheritanceStore, cfg)
	inheritanceAPI := api.NewInheritanceHandler(inheritanceStore)
	analyticsAPI := api.NewAnalyticsHandler(evidenceStore, cfg)
	blobAPI := api.NewBlobHandler(blobs, cfg.Blob.MaxBytes)
	// Mounted whether or not EVIDENCE_AUTH_DB is on. Issuing keys before
	// flipping the switch is a reasonable way to prepare a cutover, so the
	// handler reports the setting rather than refusing to work without it.
	principalAPI := api.NewPrincipalHandler(principalStore, cfg.Auth.DB)
	meAPI := api.NewMeHandler(cfg.Auth.DB, sso != nil)

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
			auth.NewDBKeyAuthenticator(principalStore, slog.Default()))
	}
	// A session reads a cookie where the others read a header, which is the
	// case the chain was built for: CI keys and human logins on one endpoint.
	// Mounted whenever the principals table is live, so a session outlives a
	// change to the login configuration rather than stranding everyone who is
	// already signed in.
	if cfg.Auth.DB {
		authenticator = append(authenticator,
			auth.NewSessionAuthenticator(sessionStore, slog.Default()))
	}

	// What ways in this deployment has, for a caller who has not come in yet.
	// Mounted whether or not SSO is configured, because "no" is an answer the
	// page needs just as much as "yes".
	r.Get("/auth/config", meAPI.AuthConfig)

	// The login flow, outside /api/v1: these are browser navigations, not API
	// calls, and /auth/login has to be reachable by somebody who is not yet
	// authenticated — which is the whole point of it.
	if sso != nil {
		ssoAPI := api.NewSSOHandler(sso, principalStore, sessionStore, cfg.Auth.OIDC, slog.Default())
		r.Get("/auth/login", ssoAPI.Login)
		r.Get("/auth/callback", ssoAPI.Callback)
		r.Post("/auth/logout", ssoAPI.Logout)
	}

	r.Route("/api/v1", func(r chi.Router) {
		r.Use(auth.Authenticate(authenticator))
		// After authentication, because it only applies to callers who arrived
		// with a cookie, and only authentication knows which those are.
		r.Use(auth.RequireCSRF)
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

		// Who is calling, for a client deciding what to offer them. The only
		// route here asserting no permission: authentication has already run,
		// and a principal holding nothing still deserves to be told so.
		r.Get("/me", meAPI.Get)

		// Administering the identities that authenticate against this store.
		// There is no delete: revocation is a timestamp, so that evidence
		// already attributed to a principal still names something.
		r.Route("/principals", func(r chi.Router) {
			r.Use(auth.Require(auth.PermPrincipalAdmin))
			r.Get("/", principalAPI.List)
			r.Post("/", principalAPI.Create)
			r.Put("/{id}/roles", principalAPI.ReplaceRoles)
			r.Post("/{id}/disable", principalAPI.Disable)
			r.Post("/{id}/enable", principalAPI.Enable)
			r.Post("/{id}/rotate", principalAPI.Rotate)
		})
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
