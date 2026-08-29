package migrate

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
)

func Run(databaseURL, migrationsPath string) error {
	m, err := migrate.New("file://"+migrationsPath, databaseURL)
	if err != nil {
		return fmt.Errorf("create migrator: %w", err)
	}
	// Close returns the source and database errors together. Migrations have
	// already run by then, so this cannot fail the call — but a database
	// connection that would not close is worth a line in the log.
	defer func() {
		if sourceErr, dbErr := m.Close(); sourceErr != nil || dbErr != nil {
			slog.Warn("closing migrator", "source_error", sourceErr, "database_error", dbErr)
		}
	}()

	if err := m.Up(); err != nil && !errors.Is(err, migrate.ErrNoChange) {
		return fmt.Errorf("run migrations: %w", err)
	}

	slog.Info("migrations applied successfully")
	return nil
}
