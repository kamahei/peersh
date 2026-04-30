// Package sqlite implements store.Store on top of modernc.org/sqlite.
//
// Pure Go (no CGO). The driver registers itself under the database/sql name
// "sqlite", which is what New() expects.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/peersh/peersh/core/store"
)

// Store is the SQLite-backed implementation of store.Store. Safe for
// concurrent use; SQLite serializes writes internally.
type Store struct {
	db *sql.DB
}

// Open opens (or creates) the SQLite database at the given path and applies
// all pending migrations. The returned Store owns the underlying *sql.DB and
// closes it via Store.Close.
//
// Pass ":memory:" for an ephemeral in-process DB (handy in tests).
func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open %q: %w", path, err)
	}
	// modernc/sqlite handles concurrency reasonably; one writer connection
	// at a time keeps SQLite from contending with itself.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if _, err := db.ExecContext(context.Background(), `PRAGMA journal_mode = WAL;`); err != nil {
		// WAL fails on :memory: — that's fine, it's a hint rather than a
		// requirement.
		_ = err
	}
	if _, err := db.ExecContext(context.Background(), `PRAGMA foreign_keys = ON;`); err != nil {
		_ = err
	}

	if err := runMigrations(db); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &Store{db: db}, nil
}

// Close closes the underlying database handle.
func (s *Store) Close() error { return s.db.Close() }

// --- timestamp helpers -----------------------------------------------------

func formatTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format(time.RFC3339Nano)
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, nil
	}
	return time.Parse(time.RFC3339Nano, s)
}

// --- Device ----------------------------------------------------------------

func (s *Store) PutDevice(ctx context.Context, d store.Device) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO devices(id, public_key, owner_user_id, kind, display_name, created_at, last_seen_at)
		VALUES(?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			public_key=excluded.public_key,
			owner_user_id=excluded.owner_user_id,
			kind=excluded.kind,
			display_name=excluded.display_name,
			last_seen_at=excluded.last_seen_at
	`, d.ID, d.PublicKey, d.OwnerUserID, int(d.Kind), d.DisplayName, formatTime(d.CreatedAt), formatTime(d.LastSeenAt))
	if err != nil {
		return fmt.Errorf("sqlite: PutDevice: %w", err)
	}
	return nil
}

func (s *Store) GetDevice(ctx context.Context, id string) (store.Device, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, public_key, owner_user_id, kind, display_name, created_at, last_seen_at
		FROM devices WHERE id = ?`, id)
	return scanDevice(row)
}

func (s *Store) ListDevicesByOwner(ctx context.Context, userID string) ([]store.Device, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, public_key, owner_user_id, kind, display_name, created_at, last_seen_at
		FROM devices WHERE owner_user_id = ?`, userID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: ListDevicesByOwner: %w", err)
	}
	defer rows.Close()
	var out []store.Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *Store) DeleteDevice(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM devices WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("sqlite: DeleteDevice: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanDevice(r rowScanner) (store.Device, error) {
	var d store.Device
	var kind int
	var createdAt, lastSeenAt string
	err := r.Scan(&d.ID, &d.PublicKey, &d.OwnerUserID, &kind, &d.DisplayName, &createdAt, &lastSeenAt)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Device{}, store.ErrNotFound
	}
	if err != nil {
		return store.Device{}, fmt.Errorf("sqlite: scanDevice: %w", err)
	}
	d.Kind = store.DeviceKind(kind)
	if d.CreatedAt, err = parseTime(createdAt); err != nil {
		return store.Device{}, err
	}
	if d.LastSeenAt, err = parseTime(lastSeenAt); err != nil {
		return store.Device{}, err
	}
	return d, nil
}

// --- Session ---------------------------------------------------------------

func (s *Store) PutSession(ctx context.Context, sess store.Session) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO sessions(id, user_id, mobile_device_id, host_device_id, state, created_at, last_active_at, idle_deadline_at)
		VALUES(?,?,?,?,?,?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			user_id=excluded.user_id,
			mobile_device_id=excluded.mobile_device_id,
			host_device_id=excluded.host_device_id,
			state=excluded.state,
			last_active_at=excluded.last_active_at,
			idle_deadline_at=excluded.idle_deadline_at
	`, sess.ID, sess.UserID, sess.MobileDeviceID, sess.HostDeviceID, int(sess.State),
		formatTime(sess.CreatedAt), formatTime(sess.LastActiveAt), formatTime(sess.IdleDeadlineAt))
	if err != nil {
		return fmt.Errorf("sqlite: PutSession: %w", err)
	}
	return nil
}

func (s *Store) GetSession(ctx context.Context, id string) (store.Session, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, user_id, mobile_device_id, host_device_id, state, created_at, last_active_at, idle_deadline_at
		FROM sessions WHERE id = ?`, id)
	var sess store.Session
	var stateInt int
	var createdAt, lastActive, idleDeadline string
	err := row.Scan(&sess.ID, &sess.UserID, &sess.MobileDeviceID, &sess.HostDeviceID,
		&stateInt, &createdAt, &lastActive, &idleDeadline)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Session{}, store.ErrNotFound
	}
	if err != nil {
		return store.Session{}, fmt.Errorf("sqlite: GetSession: %w", err)
	}
	sess.State = store.SessionState(stateInt)
	if sess.CreatedAt, err = parseTime(createdAt); err != nil {
		return store.Session{}, err
	}
	if sess.LastActiveAt, err = parseTime(lastActive); err != nil {
		return store.Session{}, err
	}
	if sess.IdleDeadlineAt, err = parseTime(idleDeadline); err != nil {
		return store.Session{}, err
	}
	return sess, nil
}

func (s *Store) DeleteSession(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("sqlite: DeleteSession: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// --- User ------------------------------------------------------------------

func (s *Store) PutUser(ctx context.Context, u store.User) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO users(id, auth_provider, created_at)
		VALUES(?,?,?)
		ON CONFLICT(id) DO UPDATE SET
			auth_provider=excluded.auth_provider
	`, u.ID, int(u.AuthProvider), formatTime(u.CreatedAt))
	if err != nil {
		return fmt.Errorf("sqlite: PutUser: %w", err)
	}
	return nil
}

func (s *Store) GetUser(ctx context.Context, id string) (store.User, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, auth_provider, created_at FROM users WHERE id = ?`, id)
	var u store.User
	var authProvider int
	var createdAt string
	err := row.Scan(&u.ID, &authProvider, &createdAt)
	if errors.Is(err, sql.ErrNoRows) {
		return store.User{}, store.ErrNotFound
	}
	if err != nil {
		return store.User{}, fmt.Errorf("sqlite: GetUser: %w", err)
	}
	u.AuthProvider = store.AuthProvider(authProvider)
	if u.CreatedAt, err = parseTime(createdAt); err != nil {
		return store.User{}, err
	}
	return u, nil
}

// --- PSKRecord -------------------------------------------------------------

func (s *Store) PutPSKRecord(ctx context.Context, r store.PSKRecord) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO psk_records(user_id, secret, display_label, created_at, revoked_at)
		VALUES(?,?,?,?,?)
		ON CONFLICT(user_id) DO UPDATE SET
			secret=excluded.secret,
			display_label=excluded.display_label,
			revoked_at=excluded.revoked_at
	`, r.UserID, r.Secret, r.DisplayLabel, formatTime(r.CreatedAt), formatTime(r.RevokedAt))
	if err != nil {
		return fmt.Errorf("sqlite: PutPSKRecord: %w", err)
	}
	return nil
}

func (s *Store) GetPSKRecord(ctx context.Context, userID string) (store.PSKRecord, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT user_id, secret, display_label, created_at, revoked_at
		FROM psk_records WHERE user_id = ?`, userID)
	return scanPSKRecord(row)
}

func (s *Store) ListPSKRecords(ctx context.Context) ([]store.PSKRecord, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT user_id, secret, display_label, created_at, revoked_at FROM psk_records`)
	if err != nil {
		return nil, fmt.Errorf("sqlite: ListPSKRecords: %w", err)
	}
	defer rows.Close()
	var out []store.PSKRecord
	for rows.Next() {
		r, err := scanPSKRecord(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *Store) DeletePSKRecord(ctx context.Context, userID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM psk_records WHERE user_id = ?`, userID)
	if err != nil {
		return fmt.Errorf("sqlite: DeletePSKRecord: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func scanPSKRecord(r rowScanner) (store.PSKRecord, error) {
	var rec store.PSKRecord
	var createdAt, revokedAt string
	err := r.Scan(&rec.UserID, &rec.Secret, &rec.DisplayLabel, &createdAt, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return store.PSKRecord{}, store.ErrNotFound
	}
	if err != nil {
		return store.PSKRecord{}, fmt.Errorf("sqlite: scanPSKRecord: %w", err)
	}
	if rec.CreatedAt, err = parseTime(createdAt); err != nil {
		return store.PSKRecord{}, err
	}
	if rec.RevokedAt, err = parseTime(revokedAt); err != nil {
		return store.PSKRecord{}, err
	}
	return rec, nil
}

// --- Pairing ---------------------------------------------------------------

func (s *Store) PutPairing(ctx context.Context, p store.Pairing) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO pairings(user_id, mobile_device_id, host_device_id, created_at, last_used_at)
		VALUES(?,?,?,?,?)
		ON CONFLICT(user_id, mobile_device_id, host_device_id) DO UPDATE SET
			last_used_at=excluded.last_used_at
	`, p.UserID, p.MobileDeviceID, p.HostDeviceID, formatTime(p.CreatedAt), formatTime(p.LastUsedAt))
	if err != nil {
		return fmt.Errorf("sqlite: PutPairing: %w", err)
	}
	return nil
}

func (s *Store) GetPairing(ctx context.Context, userID, mobileDeviceID, hostDeviceID string) (store.Pairing, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT user_id, mobile_device_id, host_device_id, created_at, last_used_at
		FROM pairings WHERE user_id = ? AND mobile_device_id = ? AND host_device_id = ?`,
		userID, mobileDeviceID, hostDeviceID)
	return scanPairing(row)
}

func (s *Store) ListPairingsByUser(ctx context.Context, userID string) ([]store.Pairing, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT user_id, mobile_device_id, host_device_id, created_at, last_used_at
		FROM pairings WHERE user_id = ?`, userID)
	if err != nil {
		return nil, fmt.Errorf("sqlite: ListPairingsByUser: %w", err)
	}
	defer rows.Close()
	var out []store.Pairing
	for rows.Next() {
		p, err := scanPairing(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) DeletePairing(ctx context.Context, userID, mobileDeviceID, hostDeviceID string) error {
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM pairings WHERE user_id = ? AND mobile_device_id = ? AND host_device_id = ?`,
		userID, mobileDeviceID, hostDeviceID)
	if err != nil {
		return fmt.Errorf("sqlite: DeletePairing: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func scanPairing(r rowScanner) (store.Pairing, error) {
	var p store.Pairing
	var createdAt, lastUsed string
	err := r.Scan(&p.UserID, &p.MobileDeviceID, &p.HostDeviceID, &createdAt, &lastUsed)
	if errors.Is(err, sql.ErrNoRows) {
		return store.Pairing{}, store.ErrNotFound
	}
	if err != nil {
		return store.Pairing{}, fmt.Errorf("sqlite: scanPairing: %w", err)
	}
	if p.CreatedAt, err = parseTime(createdAt); err != nil {
		return store.Pairing{}, err
	}
	if p.LastUsedAt, err = parseTime(lastUsed); err != nil {
		return store.Pairing{}, err
	}
	return p, nil
}
