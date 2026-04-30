package firestore

import (
	"context"
	"errors"
	"fmt"
	"time"

	fs "cloud.google.com/go/firestore"
	"google.golang.org/api/iterator"
	"google.golang.org/api/option"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/peersh/peersh/core/store"
)

// Store is the Cloud Firestore implementation of store.Store.
type Store struct {
	client *fs.Client
}

// Open constructs a Store using the given Firestore client. Caller owns
// the client's lifecycle.
func Open(client *fs.Client) *Store { return &Store{client: client} }

// OpenWithProject constructs both the Firestore client and the Store. If
// credentialsPath is empty, Application Default Credentials are used.
// The returned *Store owns the *fs.Client and closes it via Store.Close.
func OpenWithProject(ctx context.Context, projectID, credentialsPath string) (*Store, error) {
	opts := []option.ClientOption{}
	if credentialsPath != "" {
		opts = append(opts, option.WithCredentialsFile(credentialsPath))
	}
	c, err := fs.NewClient(ctx, projectID, opts...)
	if err != nil {
		return nil, fmt.Errorf("firestore: NewClient: %w", err)
	}
	return &Store{client: c}, nil
}

// Close releases the underlying Firestore client.
func (s *Store) Close() error { return s.client.Close() }

// --- helpers ---------------------------------------------------------------

func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	return st.Code() == codes.NotFound
}

func (s *Store) userDoc(userID string) *fs.DocumentRef {
	return s.client.Collection("users").Doc(userID)
}

// --- User ------------------------------------------------------------------

func (s *Store) PutUser(ctx context.Context, u store.User) error {
	_, err := s.userDoc(u.ID).Set(ctx, map[string]any{
		"auth_provider": int(u.AuthProvider),
		"created_at":    u.CreatedAt,
	}, fs.MergeAll)
	return err
}

func (s *Store) GetUser(ctx context.Context, id string) (store.User, error) {
	snap, err := s.userDoc(id).Get(ctx)
	if isNotFound(err) {
		return store.User{}, store.ErrNotFound
	}
	if err != nil {
		return store.User{}, err
	}
	data := snap.Data()
	return store.User{
		ID:           id,
		AuthProvider: store.AuthProvider(asInt(data["auth_provider"])),
		CreatedAt:    asTime(data["created_at"]),
	}, nil
}

// --- Device ----------------------------------------------------------------

func (s *Store) PutDevice(ctx context.Context, d store.Device) error {
	_, err := s.userDoc(d.OwnerUserID).Collection("devices").Doc(d.ID).Set(ctx, map[string]any{
		"public_key":   d.PublicKey,
		"kind":         int(d.Kind),
		"display_name": d.DisplayName,
		"created_at":   d.CreatedAt,
		"last_seen_at": d.LastSeenAt,
	}, fs.MergeAll)
	return err
}

func (s *Store) GetDevice(ctx context.Context, id string) (store.Device, error) {
	// Without an owner, we have to collection-group query. This costs one
	// read per matching doc; we expect at most one.
	iter := s.client.CollectionGroup("devices").Where("__name__", ">=", "").Documents(ctx)
	defer iter.Stop()
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return store.Device{}, store.ErrNotFound
		}
		if err != nil {
			return store.Device{}, err
		}
		if snap.Ref.ID != id {
			continue
		}
		// owner_user_id is the parent collection's parent doc id.
		ownerID := snap.Ref.Parent.Parent.ID
		return scanDevice(id, ownerID, snap.Data()), nil
	}
}

func (s *Store) ListDevicesByOwner(ctx context.Context, userID string) ([]store.Device, error) {
	iter := s.userDoc(userID).Collection("devices").Documents(ctx)
	defer iter.Stop()
	var out []store.Device
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		out = append(out, scanDevice(snap.Ref.ID, userID, snap.Data()))
	}
}

func (s *Store) DeleteDevice(ctx context.Context, id string) error {
	d, err := s.GetDevice(ctx, id)
	if err != nil {
		return err
	}
	_, err = s.userDoc(d.OwnerUserID).Collection("devices").Doc(id).Delete(ctx)
	return err
}

func scanDevice(id, ownerID string, data map[string]any) store.Device {
	return store.Device{
		ID:          id,
		OwnerUserID: ownerID,
		PublicKey:   asBytes(data["public_key"]),
		Kind:        store.DeviceKind(asInt(data["kind"])),
		DisplayName: asString(data["display_name"]),
		CreatedAt:   asTime(data["created_at"]),
		LastSeenAt:  asTime(data["last_seen_at"]),
	}
}

// --- Session ---------------------------------------------------------------

func (s *Store) PutSession(ctx context.Context, sess store.Session) error {
	_, err := s.userDoc(sess.UserID).Collection("sessions").Doc(sess.ID).Set(ctx, map[string]any{
		"mobile_device_id": sess.MobileDeviceID,
		"host_device_id":   sess.HostDeviceID,
		"state":            int(sess.State),
		"created_at":       sess.CreatedAt,
		"last_active_at":   sess.LastActiveAt,
		"idle_deadline_at": sess.IdleDeadlineAt,
	}, fs.MergeAll)
	return err
}

func (s *Store) GetSession(ctx context.Context, id string) (store.Session, error) {
	iter := s.client.CollectionGroup("sessions").Documents(ctx)
	defer iter.Stop()
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return store.Session{}, store.ErrNotFound
		}
		if err != nil {
			return store.Session{}, err
		}
		if snap.Ref.ID != id {
			continue
		}
		userID := snap.Ref.Parent.Parent.ID
		data := snap.Data()
		return store.Session{
			ID:             id,
			UserID:         userID,
			MobileDeviceID: asString(data["mobile_device_id"]),
			HostDeviceID:   asString(data["host_device_id"]),
			State:          store.SessionState(asInt(data["state"])),
			CreatedAt:      asTime(data["created_at"]),
			LastActiveAt:   asTime(data["last_active_at"]),
			IdleDeadlineAt: asTime(data["idle_deadline_at"]),
		}, nil
	}
}

func (s *Store) DeleteSession(ctx context.Context, id string) error {
	sess, err := s.GetSession(ctx, id)
	if err != nil {
		return err
	}
	_, err = s.userDoc(sess.UserID).Collection("sessions").Doc(id).Delete(ctx)
	return err
}

// --- PSKRecord -------------------------------------------------------------

// Firebase mode does not store PSK records. The methods below honor the
// Store interface contract by returning store.ErrNotFound.
func (s *Store) PutPSKRecord(_ context.Context, _ store.PSKRecord) error {
	return errors.New("firestore: PSK records not supported in Firebase mode")
}
func (s *Store) GetPSKRecord(_ context.Context, _ string) (store.PSKRecord, error) {
	return store.PSKRecord{}, store.ErrNotFound
}
func (s *Store) ListPSKRecords(_ context.Context) ([]store.PSKRecord, error) {
	return nil, nil
}
func (s *Store) DeletePSKRecord(_ context.Context, _ string) error {
	return store.ErrNotFound
}

// --- Pairing ---------------------------------------------------------------

func (s *Store) PutPairing(ctx context.Context, p store.Pairing) error {
	id := pairingID(p.MobileDeviceID, p.HostDeviceID)
	_, err := s.userDoc(p.UserID).Collection("pairings").Doc(id).Set(ctx, map[string]any{
		"mobile_device_id": p.MobileDeviceID,
		"host_device_id":   p.HostDeviceID,
		"created_at":       p.CreatedAt,
		"last_used_at":     p.LastUsedAt,
	}, fs.MergeAll)
	return err
}

func (s *Store) GetPairing(ctx context.Context, userID, mobileDeviceID, hostDeviceID string) (store.Pairing, error) {
	id := pairingID(mobileDeviceID, hostDeviceID)
	snap, err := s.userDoc(userID).Collection("pairings").Doc(id).Get(ctx)
	if isNotFound(err) {
		return store.Pairing{}, store.ErrNotFound
	}
	if err != nil {
		return store.Pairing{}, err
	}
	d := snap.Data()
	return store.Pairing{
		UserID:         userID,
		MobileDeviceID: asString(d["mobile_device_id"]),
		HostDeviceID:   asString(d["host_device_id"]),
		CreatedAt:      asTime(d["created_at"]),
		LastUsedAt:     asTime(d["last_used_at"]),
	}, nil
}

func (s *Store) ListPairingsByUser(ctx context.Context, userID string) ([]store.Pairing, error) {
	iter := s.userDoc(userID).Collection("pairings").Documents(ctx)
	defer iter.Stop()
	var out []store.Pairing
	for {
		snap, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		d := snap.Data()
		out = append(out, store.Pairing{
			UserID:         userID,
			MobileDeviceID: asString(d["mobile_device_id"]),
			HostDeviceID:   asString(d["host_device_id"]),
			CreatedAt:      asTime(d["created_at"]),
			LastUsedAt:     asTime(d["last_used_at"]),
		})
	}
}

func (s *Store) DeletePairing(ctx context.Context, userID, mobileDeviceID, hostDeviceID string) error {
	id := pairingID(mobileDeviceID, hostDeviceID)
	_, err := s.userDoc(userID).Collection("pairings").Doc(id).Delete(ctx)
	if isNotFound(err) {
		return store.ErrNotFound
	}
	return err
}

func pairingID(mobile, host string) string { return mobile + "__" + host }

// --- value coercions -------------------------------------------------------

func asInt(v any) int {
	switch x := v.(type) {
	case int64:
		return int(x)
	case int:
		return x
	case float64:
		return int(x)
	default:
		return 0
	}
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func asBytes(v any) []byte {
	if b, ok := v.([]byte); ok {
		return b
	}
	return nil
}

func asTime(v any) time.Time {
	if t, ok := v.(time.Time); ok {
		return t
	}
	return time.Time{}
}
