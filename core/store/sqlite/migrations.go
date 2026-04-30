package sqlite

import (
	"database/sql"
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// runMigrations creates schema_migrations if it doesn't exist, then applies
// any embedded migration whose version isn't yet recorded. Filenames are
// expected to match `NNNN_*.sql` where NNNN is a zero-padded version number.
func runMigrations(db *sql.DB) error {
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER NOT NULL PRIMARY KEY,
		applied_at TEXT NOT NULL
	)`); err != nil {
		return fmt.Errorf("sqlite: ensure schema_migrations: %w", err)
	}

	applied := map[int]bool{}
	rows, err := db.Query(`SELECT version FROM schema_migrations`)
	if err != nil {
		return fmt.Errorf("sqlite: read schema_migrations: %w", err)
	}
	for rows.Next() {
		var v int
		if err := rows.Scan(&v); err != nil {
			rows.Close()
			return fmt.Errorf("sqlite: scan version: %w", err)
		}
		applied[v] = true
	}
	rows.Close()

	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("sqlite: list migrations: %w", err)
	}
	type migration struct {
		version int
		name    string
		body    []byte
	}
	var pending []migration
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		v, err := parseMigrationVersion(e.Name())
		if err != nil {
			return err
		}
		if applied[v] {
			continue
		}
		body, err := migrationsFS.ReadFile("migrations/" + e.Name())
		if err != nil {
			return fmt.Errorf("sqlite: read %s: %w", e.Name(), err)
		}
		pending = append(pending, migration{v, e.Name(), body})
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].version < pending[j].version })

	for _, m := range pending {
		if _, err := db.Exec(string(m.body)); err != nil {
			return fmt.Errorf("sqlite: apply %s: %w", m.name, err)
		}
		if _, err := db.Exec(
			`INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`,
			m.version, time.Now().UTC().Format(time.RFC3339Nano),
		); err != nil {
			return fmt.Errorf("sqlite: record %s: %w", m.name, err)
		}
	}
	return nil
}

// parseMigrationVersion extracts the leading integer from filenames like
// "0001_initial.sql".
func parseMigrationVersion(name string) (int, error) {
	for i := 0; i < len(name); i++ {
		if name[i] < '0' || name[i] > '9' {
			if i == 0 {
				return 0, fmt.Errorf("sqlite: migration %q: missing version prefix", name)
			}
			v, err := strconv.Atoi(name[:i])
			if err != nil {
				return 0, fmt.Errorf("sqlite: migration %q: %w", name, err)
			}
			return v, nil
		}
	}
	return 0, fmt.Errorf("sqlite: migration %q: malformed", name)
}
