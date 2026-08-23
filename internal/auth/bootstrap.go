package auth

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/nesono/evidence-store/internal/model"
)

// PrincipalRegistry is the writing half of the store, needed only at startup.
// It is separate from PrincipalLookup so that the authentication path holds an
// interface that cannot create an administrator.
type PrincipalRegistry interface {
	FindBySubject(ctx context.Context, subject string) (*model.Principal, error)
	// Insert creates the principal and its roles together, and returns
	// (nil, nil) when the subject is already taken.
	Insert(ctx context.Context, in model.PrincipalCreate) (*model.Principal, error)
}

// BootstrapAdmin makes database-backed authentication usable on an empty
// database. Switching it on closes every endpoint, and the API for issuing the
// first key is itself behind principal:admin — so without this an operator
// would have to reach for psql to get in at all.
//
// It seeds exactly one administrator, named by subject, and mints its key. The
// plaintext is returned so the caller can put it in front of the operator once;
// only the hash is stored, and this is the single moment the key can be read.
// Running again with the subject already present does nothing and returns "",
// which is what makes it safe on every start and on every replica.
func BootstrapAdmin(ctx context.Context, registry PrincipalRegistry, subject string, logger *slog.Logger) (string, error) {
	if logger == nil {
		logger = slog.Default()
	}

	existing, err := registry.FindBySubject(ctx, subject)
	if err != nil {
		return "", fmt.Errorf("look up bootstrap admin: %w", err)
	}
	if existing != nil {
		return "", nil
	}

	key, err := GenerateKey()
	if err != nil {
		return "", err
	}

	created, err := registry.Insert(ctx, model.PrincipalCreate{
		Subject:     subject,
		Kind:        model.PrincipalKindAPIKey,
		DisplayName: "bootstrap administrator",
		KeyHash:     HashKey(key),
		Roles:       []string{string(RoleAdmin)},
		// GrantedBy stays nil: there is by definition no principal yet to
		// credit with the grant.
	})
	if err != nil {
		return "", fmt.Errorf("create bootstrap admin: %w", err)
	}
	if created == nil {
		// Another replica won the race between the lookup and the insert. Its
		// key is the real one; the one minted here was never stored.
		return "", nil
	}

	logger.Info("bootstrap admin created", "subject", subject, "principal_id", created.ID)
	return key, nil
}
