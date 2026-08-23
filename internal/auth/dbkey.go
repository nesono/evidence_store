package auth

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nesono/evidence-store/internal/model"
)

// PrincipalLookup is the database seam the authenticator needs, and no more of
// the store than that. Written as an interface here rather than taken as a
// concrete type so that a test can answer without a container, and so that
// internal/store stays unaware of Principal, Role, and Permission.
type PrincipalLookup interface {
	// FindByKeyHash resolves a hashed bearer token. A token matching nothing
	// returns (nil, nil): unknown is an answer, and an error means the lookup
	// itself failed.
	FindByKeyHash(ctx context.Context, keyHash string) (*model.Principal, error)
	// TouchLastSeen records that a principal authenticated. It is best effort;
	// a request does not fail because bookkeeping did.
	TouchLastSeen(ctx context.Context, id uuid.UUID) error
}

// DBKeyAuthenticator authenticates bearer tokens against the principals table.
// This is what turns a shared secret into an identity: a key here has a name,
// an owner, a role set granted individually, and a revocation that takes effect
// on the next request rather than the next redeploy.
//
// It is never disabled. An operator who switched database-backed authentication
// on has closed the door, and an empty principals table means nobody may in —
// not that everybody may.
type DBKeyAuthenticator struct {
	lookup PrincipalLookup
	logger *slog.Logger

	// touched throttles the last_seen_at write per principal. The store
	// throttles too, in a predicate replicas share; this saves sending the
	// statement at all on the hot path.
	touchedMu sync.Mutex
	touched   map[uuid.UUID]time.Time
}

// touchEvery is how often one process will bother recording that a principal
// is alive. Deliberately shorter than the store's own predicate, which is the
// authority — this is a filter, not a schedule.
const touchEvery = 30 * time.Second

func NewDBKeyAuthenticator(lookup PrincipalLookup, logger *slog.Logger) *DBKeyAuthenticator {
	if logger == nil {
		logger = slog.Default()
	}
	return &DBKeyAuthenticator{
		lookup:  lookup,
		logger:  logger,
		touched: make(map[uuid.UUID]time.Time),
	}
}

func (a *DBKeyAuthenticator) Authenticate(ctx context.Context, r *http.Request) (*Principal, error) {
	token := extractBearer(r)
	if token == "" {
		return nil, ErrNoCredentials
	}
	// A credential that cannot be one of ours — a legacy env-var key, or a
	// session token in phase 5 — is not worth a query. Report it as unread
	// rather than wrong so the chain keeps looking.
	if !LooksLikeKey(token) {
		return nil, ErrNoCredentials
	}

	stored, err := a.lookup.FindByKeyHash(ctx, HashKey(token))
	if err != nil {
		// The difference between "we cannot check" and "we checked and no"
		// matters to whoever is paged: one is a 503 and an outage, the other is
		// a 401 and a typo.
		return nil, fmt.Errorf("%w: %w", ErrAuthUnavailable, err)
	}
	if stored == nil {
		return nil, ErrInvalidCredentials
	}
	if stored.Disabled() {
		// Worth a line: a revoked key still in use is either a pipeline nobody
		// updated or somebody trying one they should not have.
		a.logger.Info("rejected disabled principal", "subject", stored.Subject)
		return nil, ErrInvalidCredentials
	}

	a.touch(ctx, stored.ID)
	return principalFromStored(stored), nil
}

// principalFromStored converts a row into the identity the request carries.
//
// Unknown role names are dropped rather than rejected. A binding can outlive
// the constant it names — a downgrade, a half-finished rollout — and the safe
// reading of a role this binary does not define is that it grants nothing,
// which is also what Role.Grants would conclude.
func principalFromStored(stored *model.Principal) *Principal {
	roles := make([]Role, 0, len(stored.Roles))
	for _, name := range stored.Roles {
		if role, ok := ParseRole(name); ok {
			roles = append(roles, role)
		}
	}
	p := NewPrincipal(stored.Subject, ParseKind(stored.Kind), stored.DisplayName, roles...)
	p.ID = stored.ID
	return p
}

// touch records liveness without making the caller wait for it. The write is
// bookkeeping on the authentication path of every request, so it runs detached
// from the request's own context — which is about to be cancelled the moment
// the response is written.
func (a *DBKeyAuthenticator) touch(ctx context.Context, id uuid.UUID) {
	now := time.Now()
	a.touchedMu.Lock()
	last, seen := a.touched[id]
	if seen && now.Sub(last) < touchEvery {
		a.touchedMu.Unlock()
		return
	}
	a.touched[id] = now
	a.touchedMu.Unlock()

	detached := context.WithoutCancel(ctx)
	go func() {
		ctx, cancel := context.WithTimeout(detached, 5*time.Second)
		defer cancel()
		if err := a.lookup.TouchLastSeen(ctx, id); err != nil {
			a.logger.Warn("failed to record principal last_seen_at", "principal_id", id, "error", err)
		}
	}()
}
