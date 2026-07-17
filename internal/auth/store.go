package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"
)

type Role string

const (
	RoleAdmin  Role = "admin"
	RoleClient Role = "client"
)

const (
	bootstrapID     = "bootstrap"
	bootstrapLabel  = "Bootstrap administrator"
	maxLabelBytes   = 128
	persistInterval = time.Hour
)

var (
	ErrBootstrapToken = errors.New("bootstrap token cannot be revoked")
	ErrTokenNotFound  = errors.New("token not found")
	ErrInvalidLabel   = errors.New("invalid token label")
	ErrDuplicateID    = errors.New("duplicate token ID")
)

type Principal struct {
	TokenID string
	Role    Role
}

type TokenMeta struct {
	ID         string     `json:"id"`
	Label      string     `json:"label"`
	CreatedAt  time.Time  `json:"created_at"`
	LastSeenAt *time.Time `json:"last_seen_at"`
	Bootstrap  bool       `json:"bootstrap"`
}

type Option func(*Store)

// WithClock supplies a clock for deterministic callers and tests.
func WithClock(clock func() time.Time) Option {
	return func(store *Store) {
		if clock != nil {
			store.now = clock
		}
	}
}

// WithRandom supplies the cryptographically secure byte source used by Issue.
func WithRandom(random io.Reader) Option {
	return func(store *Store) {
		if random != nil {
			store.random = random
		}
	}
}

type Store struct {
	mu                  sync.RWMutex
	path                string
	now                 func() time.Time
	random              io.Reader
	records             map[string]*tokenRecord
	lastSeenPersistedAt time.Time
}

type tokenRecord struct {
	TokenMeta
	Hash              string     `json:"hash"`
	PersistedLastSeen *time.Time `json:"-"`
}

type diskStore struct {
	Tokens []tokenRecord `json:"tokens"`
}

func OpenStore(path, bootstrap string, opts ...Option) (*Store, error) {
	if path == "" {
		return nil, errors.New("token store path is empty")
	}
	if bootstrap == "" {
		return nil, errors.New("bootstrap token is empty")
	}
	store := &Store{
		path:    path,
		now:     time.Now,
		random:  rand.Reader,
		records: make(map[string]*tokenRecord),
	}
	for _, option := range opts {
		if option != nil {
			option(store)
		}
	}

	changed := false
	raw, err := os.ReadFile(path)
	switch {
	case err == nil:
		var disk diskStore
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&disk); err != nil {
			return nil, fmt.Errorf("decode token store: %w", err)
		}
		if decoder.Decode(&struct{}{}) != io.EOF {
			return nil, errors.New("decode token store: trailing JSON")
		}
		for i := range disk.Tokens {
			record := disk.Tokens[i]
			if err := validateRecord(record); err != nil {
				return nil, fmt.Errorf("decode token store: %w", err)
			}
			if _, exists := store.records[record.ID]; exists {
				return nil, fmt.Errorf("decode token store: %w: %s", ErrDuplicateID, record.ID)
			}
			record.LastSeenAt = copyTime(record.LastSeenAt)
			record.PersistedLastSeen = copyTime(record.LastSeenAt)
			if record.LastSeenAt != nil && record.LastSeenAt.After(store.lastSeenPersistedAt) {
				store.lastSeenPersistedAt = *record.LastSeenAt
			}
			store.records[record.ID] = &record
		}
		if info, statErr := os.Stat(path); statErr != nil {
			return nil, fmt.Errorf("stat token store: %w", statErr)
		} else if info.Mode().Perm() != 0o600 {
			changed = true
		}
	case errors.Is(err, os.ErrNotExist):
		changed = true
	default:
		return nil, fmt.Errorf("read token store: %w", err)
	}

	now := store.now().UTC()
	bootstrapHash := hashSecret(bootstrap)
	if record, exists := store.records[bootstrapID]; exists {
		if !record.Bootstrap {
			return nil, errors.New("decode token store: bootstrap ID belongs to managed token")
		}
		if record.Hash != bootstrapHash || record.Label != bootstrapLabel {
			record.Hash = bootstrapHash
			record.Label = bootstrapLabel
			changed = true
		}
	} else {
		store.records[bootstrapID] = &tokenRecord{
			TokenMeta: TokenMeta{ID: bootstrapID, Label: bootstrapLabel, CreatedAt: now, Bootstrap: true},
			Hash:      bootstrapHash,
		}
		changed = true
	}
	if changed {
		if err := store.persistLocked(false); err != nil {
			return nil, err
		}
	}
	return store, nil
}

func (s *Store) Authenticate(secret string) (Principal, bool) {
	hash := hashSecret(secret)
	s.mu.Lock()
	defer s.mu.Unlock()

	var matched *tokenRecord
	for _, record := range s.records {
		if subtle.ConstantTimeCompare([]byte(hash), []byte(record.Hash)) == 1 {
			matched = record
		}
	}
	if matched == nil {
		return Principal{}, false
	}

	now := s.now().UTC()
	matched.LastSeenAt = &now
	if s.lastSeenPersistedAt.IsZero() || now.Sub(s.lastSeenPersistedAt) >= persistInterval {
		if err := s.persistLocked(true); err == nil {
			s.lastSeenPersistedAt = now
			for _, record := range s.records {
				record.PersistedLastSeen = copyTime(record.LastSeenAt)
			}
		}
	}
	role := RoleClient
	if matched.Bootstrap {
		role = RoleAdmin
	}
	return Principal{TokenID: matched.ID, Role: role}, true
}

func (s *Store) Issue(label string) (secret string, meta TokenMeta, err error) {
	if err := validateLabel(label); err != nil {
		return "", TokenMeta{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	raw := make([]byte, 32)
	if _, err := io.ReadFull(s.random, raw); err != nil {
		return "", TokenMeta{}, fmt.Errorf("generate token: %w", err)
	}
	payload := hex.EncodeToString(raw)
	secret = "wlt_" + payload
	meta = TokenMeta{
		ID:        payload[:16],
		Label:     label,
		CreatedAt: s.now().UTC(),
	}
	record := &tokenRecord{TokenMeta: meta, Hash: hashSecret(secret)}

	if _, exists := s.records[meta.ID]; exists {
		return "", TokenMeta{}, ErrDuplicateID
	}
	s.records[meta.ID] = record
	if err := s.persistLocked(false); err != nil {
		delete(s.records, meta.ID)
		return "", TokenMeta{}, err
	}
	return secret, copyMeta(meta), nil
}

func (s *Store) List() []TokenMeta {
	s.mu.RLock()
	defer s.mu.RUnlock()
	metas := make([]TokenMeta, 0, len(s.records))
	for _, record := range s.records {
		metas = append(metas, copyMeta(record.TokenMeta))
	}
	sort.Slice(metas, func(i, j int) bool {
		if metas[i].Bootstrap != metas[j].Bootstrap {
			return metas[i].Bootstrap
		}
		if !metas[i].CreatedAt.Equal(metas[j].CreatedAt) {
			return metas[i].CreatedAt.Before(metas[j].CreatedAt)
		}
		return metas[i].ID < metas[j].ID
	})
	return metas
}

func (s *Store) Revoke(id string) error {
	if id == bootstrapID {
		return ErrBootstrapToken
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record, exists := s.records[id]
	if !exists || record.Bootstrap {
		return ErrTokenNotFound
	}
	delete(s.records, id)
	if err := s.persistLocked(false); err != nil {
		s.records[id] = record
		return err
	}
	return nil
}

func (s *Store) persistLocked(flushLastSeen bool) error {
	disk := diskStore{Tokens: make([]tokenRecord, 0, len(s.records))}
	for _, record := range s.records {
		copy := *record
		if flushLastSeen {
			copy.LastSeenAt = copyTime(record.LastSeenAt)
		} else {
			copy.LastSeenAt = copyTime(record.PersistedLastSeen)
		}
		copy.PersistedLastSeen = nil
		disk.Tokens = append(disk.Tokens, copy)
	}
	sort.Slice(disk.Tokens, func(i, j int) bool {
		if disk.Tokens[i].Bootstrap != disk.Tokens[j].Bootstrap {
			return disk.Tokens[i].Bootstrap
		}
		return disk.Tokens[i].ID < disk.Tokens[j].ID
	})
	raw, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return fmt.Errorf("encode token store: %w", err)
	}
	raw = append(raw, '\n')

	dir := filepath.Dir(s.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create token store directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".tokens-*")
	if err != nil {
		return fmt.Errorf("create temporary token store: %w", err)
	}
	tempName := temp.Name()
	keep := false
	defer func() {
		_ = temp.Close()
		if !keep {
			_ = os.Remove(tempName)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		return fmt.Errorf("set temporary token store mode: %w", err)
	}
	if _, err := temp.Write(raw); err != nil {
		return fmt.Errorf("write temporary token store: %w", err)
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync temporary token store: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary token store: %w", err)
	}
	if err := os.Rename(tempName, s.path); err != nil {
		return fmt.Errorf("replace token store: %w", err)
	}
	keep = true
	if err := os.Chmod(s.path, 0o600); err != nil {
		return fmt.Errorf("set token store mode: %w", err)
	}
	if directory, err := os.Open(dir); err == nil {
		_ = directory.Sync()
		_ = directory.Close()
	}
	return nil
}

func validateRecord(record tokenRecord) error {
	if record.ID == bootstrapID {
		if !record.Bootstrap {
			return errors.New("bootstrap ID is not marked bootstrap")
		}
	} else {
		if record.Bootstrap || len(record.ID) != 16 || record.ID != strings.ToLower(record.ID) {
			return fmt.Errorf("invalid token ID %q", record.ID)
		}
		if _, err := hex.DecodeString(record.ID); err != nil {
			return fmt.Errorf("invalid token ID %q", record.ID)
		}
		if err := validateLabel(record.Label); err != nil {
			return err
		}
	}
	if record.CreatedAt.IsZero() {
		return fmt.Errorf("token %q has zero creation time", record.ID)
	}
	if len(record.Hash) != sha256.Size*2 || record.Hash != strings.ToLower(record.Hash) {
		return fmt.Errorf("token %q has invalid hash", record.ID)
	}
	if _, err := hex.DecodeString(record.Hash); err != nil {
		return fmt.Errorf("token %q has invalid hash", record.ID)
	}
	return nil
}

func validateLabel(label string) error {
	if label == "" || len(label) > maxLabelBytes || !utf8.ValidString(label) || strings.TrimSpace(label) != label {
		return ErrInvalidLabel
	}
	for _, r := range label {
		if unicode.IsControl(r) {
			return ErrInvalidLabel
		}
	}
	return nil
}

func hashSecret(secret string) string {
	hash := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(hash[:])
}

func copyMeta(meta TokenMeta) TokenMeta {
	meta.LastSeenAt = copyTime(meta.LastSeenAt)
	return meta
}

func copyTime(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}
