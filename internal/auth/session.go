package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/nesono/evidence-store/internal/model"
)

// SessionCookie is the cookie a logged-in browser carries. HttpOnly, so script
// on the page cannot read it — including script an attacker got onto the page.
const SessionCookie = "evidence_session"

// SessionLookup is the database seam the session authenticator needs.
type SessionLookup interface {
	// FindPrincipalBySessionToken resolves a hashed cookie value to whoever it
	// belongs to. A token matching nothing returns (nil, nil, nil): unknown,
	// expired and logged-out are all the same answer, and an error means the
	// lookup itself failed.
	FindPrincipalBySessionToken(ctx context.Context, tokenHash string) (*model.Principal, *model.Session, error)
	TouchLastSeen(ctx context.Context, id uuid.UUID) error
}

// SessionAuthenticator resolves the session cookie a human's login leaves
// behind. It is a separate scheme from the bearer-token authenticators, reading
// a different part of the request, which is exactly the case the chain was
// built for: CI keys and human logins on the same endpoints.
//
// Like the database key authenticator it is never disabled. Sessions only exist
// where a login flow made one, and a store with no login flow simply never has
// a cookie to read.
type SessionAuthenticator struct {
	lookup SessionLookup
	logger *slog.Logger

	touchedMu sync.Mutex
	touched   map[uuid.UUID]time.Time
}

func NewSessionAuthenticator(lookup SessionLookup, logger *slog.Logger) *SessionAuthenticator {
	if logger == nil {
		logger = slog.Default()
	}
	return &SessionAuthenticator{
		lookup:  lookup,
		logger:  logger,
		touched: make(map[uuid.UUID]time.Time),
	}
}

func (a *SessionAuthenticator) Authenticate(ctx context.Context, r *http.Request) (*Principal, error) {
	cookie, err := r.Cookie(SessionCookie)
	if err != nil || cookie.Value == "" {
		return nil, ErrNoCredentials
	}

	stored, session, err := a.lookup.FindPrincipalBySessionToken(ctx, HashKey(cookie.Value))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrAuthUnavailable, err)
	}
	if stored == nil {
		// Expired, logged out, or never ours. All the same to the caller: log
		// in again.
		return nil, ErrInvalidCredentials
	}
	if stored.Disabled() {
		// The point of storing sessions rather than signing them. A revoked
		// person's open browser stops working here, not when a token expires.
		a.logger.Info("rejected session of disabled principal", "subject", stored.Subject)
		return nil, ErrInvalidCredentials
	}

	a.touch(ctx, session.ID)

	p := principalFromStored(stored)
	p.ViaSession = true
	return p, nil
}

func (a *SessionAuthenticator) touch(ctx context.Context, id uuid.UUID) {
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
			a.logger.Warn("failed to record session last_seen_at", "session_id", id, "error", err)
		}
	}()
}

// GenerateSessionToken mints the value a session cookie carries: 256 bits from
// crypto/rand, hashed with HashKey before it is stored, for the same reasons
// API keys are.
func GenerateSessionToken() (string, error) {
	b := make([]byte, keyBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
