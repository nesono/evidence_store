package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SAMLRequestStore remembers logins that have gone out to a SAML provider and
// not yet come back.
//
// A service provider must refuse an assertion answering a request nobody made,
// and SAML gives it nowhere client-side to keep that: the assertion arrives as
// a form POST from the provider's origin, which is exactly the request a
// SameSite=Lax cookie is not sent on. Keeping the ids here rather than
// loosening the session cookie also means a login survives landing on a
// different replica than the one that started it.
type SAMLRequestStore struct {
	pool *pgxpool.Pool
}

func NewSAMLRequestStore(pool *pgxpool.Pool) *SAMLRequestStore {
	return &SAMLRequestStore{pool: pool}
}

// Remember records an outstanding authentication request.
func (s *SAMLRequestStore) Remember(ctx context.Context, id string, expiresAt time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO saml_requests (id, expires_at) VALUES ($1, $2)
		ON CONFLICT (id) DO NOTHING
	`, id, expiresAt)
	if err != nil {
		return fmt.Errorf("remember saml request: %w", err)
	}
	return nil
}

// Pending returns every request still in flight, which is what the assertion's
// InResponseTo is checked against.
//
// All of them, rather than the one belonging to this browser, because the POST
// carries nothing of ours to say which browser that is. What the check rules
// out is an assertion for a login nobody here started, or one replayed after
// its window closed; the signature is what rules out the rest.
func (s *SAMLRequestStore) Pending(ctx context.Context) ([]string, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT id FROM saml_requests WHERE expires_at > now()`)
	if err != nil {
		return nil, fmt.Errorf("list pending saml requests: %w", err)
	}
	defer rows.Close()

	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan pending saml request: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// Forget removes a request once its assertion has been accepted, so the same
// assertion cannot be presented twice.
func (s *SAMLRequestStore) Forget(ctx context.Context, id string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM saml_requests WHERE id = $1`, id); err != nil {
		return fmt.Errorf("forget saml request: %w", err)
	}
	return nil
}

// DeleteExpired clears out logins nobody finished. Expired ids are already
// refused, so this is housekeeping rather than security.
func (s *SAMLRequestStore) DeleteExpired(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM saml_requests WHERE expires_at <= now()`)
	if err != nil {
		return 0, fmt.Errorf("delete expired saml requests: %w", err)
	}
	return tag.RowsAffected(), nil
}
