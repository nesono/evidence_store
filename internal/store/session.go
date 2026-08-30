package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/nesono/evidence-store/internal/model"
)

// SessionStore holds logged-in browsers.
//
// A session is a row and not a signed cookie because the store has promised
// immediate revocation since phase 2: disabling a principal stops their API key
// on the next request, and it would be strange for their browser to keep
// working until a token expired. The cost is one indexed read per request from
// a human, which is a rounding error against the CI traffic this store is sized
// for.
type SessionStore struct {
	pool *pgxpool.Pool
}

func NewSessionStore(pool *pgxpool.Pool) *SessionStore {
	return &SessionStore{pool: pool}
}

// Create records a new session. The caller mints the token and hashes it, so
// this package never holds the value a cookie carries.
//
// idToken is the ID token the login arrived on, kept so that logging out can
// end the provider's session as well as this one. Empty is fine and means there
// is no provider logout to perform.
func (s *SessionStore) Create(ctx context.Context, principalID uuid.UUID, tokenHash string, expiresAt time.Time, userAgent, idToken string) (*model.Session, error) {
	var sess model.Session
	err := s.pool.QueryRow(ctx, `
		INSERT INTO sessions (principal_id, token_hash, expires_at, user_agent, id_token)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id, principal_id, created_at, expires_at, last_seen_at, user_agent
	`, principalID, tokenHash, expiresAt, userAgent, idToken).Scan(
		&sess.ID, &sess.PrincipalID, &sess.CreatedAt, &sess.ExpiresAt, &sess.LastSeenAt, &sess.UserAgent,
	)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	return &sess, nil
}

// FindPrincipalBySessionToken resolves a cookie to whoever it belongs to, in
// one query: the session, its principal, and that principal's roles.
//
// Expiry is asserted here rather than left to the sweep, so a session is dead
// the moment it is due to be even if nothing has cleaned up yet. A token
// matching nothing — unknown, expired, or logged out — returns (nil, nil, nil):
// an answer, not a failure, which is what keeps a database outage
// distinguishable from a stale cookie.
func (s *SessionStore) FindPrincipalBySessionToken(ctx context.Context, tokenHash string) (*model.Principal, *model.Session, error) {
	var p model.Principal
	var sess model.Session
	err := s.pool.QueryRow(ctx, `
		SELECT s.id, s.principal_id, s.created_at, s.expires_at, s.last_seen_at, s.user_agent,
		       p.id, p.subject, p.kind, p.display_name,
		       p.disabled_at, p.created_at, p.last_seen_at,
		       COALESCE(
		           ARRAY_AGG(rb.role ORDER BY rb.role) FILTER (WHERE rb.role IS NOT NULL),
		           '{}'
		       ) AS roles
		FROM sessions s
		JOIN principals p ON p.id = s.principal_id
		LEFT JOIN role_bindings rb ON rb.principal_id = p.id AND rb.scope = $2
		WHERE s.token_hash = $1 AND s.expires_at > now()
		GROUP BY s.id, p.id
	`, tokenHash, model.ScopeStoreWide).Scan(
		&sess.ID, &sess.PrincipalID, &sess.CreatedAt, &sess.ExpiresAt, &sess.LastSeenAt, &sess.UserAgent,
		&p.ID, &p.Subject, &p.Kind, &p.DisplayName,
		&p.DisabledAt, &p.CreatedAt, &p.LastSeenAt, &p.Roles,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("query session: %w", err)
	}
	return &p, &sess, nil
}

// EndedSession is what a logout took away: enough to end the provider's session
// too, and enough to say in the log whose session it was.
type EndedSession struct {
	// IDToken is the token that login arrived on, for id_token_hint. Empty for
	// a SAML login or a session created before the column existed.
	IDToken string
	Subject string
}

// Delete ends one session — the logout button — and reports what it ended, so
// the caller can go on to end the provider's session and name whose it was.
// Deleting a session that is already gone is not an error; the caller wanted it
// gone either way, and the zero value means there is nothing further to do.
//
// One statement rather than a read followed by a delete: two would leave room
// for a second logout of the same session to find it still there, and the
// subject is wanted precisely because this route runs outside the
// authentication middleware and so has no principal in hand.
func (s *SessionStore) Delete(ctx context.Context, tokenHash string) (EndedSession, error) {
	var ended EndedSession
	err := s.pool.QueryRow(ctx, `
		WITH gone AS (
		    DELETE FROM sessions WHERE token_hash = $1
		    RETURNING id_token, principal_id
		)
		SELECT gone.id_token, p.subject
		FROM gone JOIN principals p ON p.id = gone.principal_id
	`, tokenHash).Scan(&ended.IDToken, &ended.Subject)
	if errors.Is(err, pgx.ErrNoRows) {
		return EndedSession{}, nil
	}
	if err != nil {
		return EndedSession{}, fmt.Errorf("delete session: %w", err)
	}
	return ended, nil
}

// DeleteForPrincipal ends every session a principal has. This is what makes
// revocation whole: disabling somebody already stops their next request, and
// this stops the browsers they left open from being restored by an enable.
func (s *SessionStore) DeleteForPrincipal(ctx context.Context, principalID uuid.UUID) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE principal_id = $1`, principalID); err != nil {
		return fmt.Errorf("delete sessions for principal: %w", err)
	}
	return nil
}

// TouchLastSeen records that a session was used, throttled in the same way and
// for the same reason as principals.last_seen_at.
func (s *SessionStore) TouchLastSeen(ctx context.Context, id uuid.UUID) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE sessions
		SET last_seen_at = now()
		WHERE id = $1
		  AND (last_seen_at IS NULL OR last_seen_at < now() - INTERVAL '`+touchInterval+`')
	`, id)
	if err != nil {
		return fmt.Errorf("touch session last_seen_at: %w", err)
	}
	return nil
}

// DeleteExpired clears out what has timed out. Expired sessions already fail to
// authenticate, so this is housekeeping rather than security — it keeps the
// table from growing without bound one abandoned login at a time.
func (s *SessionStore) DeleteExpired(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM sessions WHERE expires_at <= now()`)
	if err != nil {
		return 0, fmt.Errorf("delete expired sessions: %w", err)
	}
	return tag.RowsAffected(), nil
}
