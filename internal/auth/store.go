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
	ErrDuplicateHash  = errors.New("duplicate token hash")
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

func withClock(clock func() time.Time) Option {
	return func(store *Store) {
		if clock != nil {
			store.now = clock
		}
	}
}

func withRandom(random io.Reader) Option {
	return func(store *Store) {
		if random != nil {
			store.random = random
		}
	}
}

type Store struct {
	mu                   sync.RWMutex
	path                 string
	now                  func() time.Time
	random               io.Reader
	fs                   fileSystem
	records              map[string]*tokenRecord
	lastSeenPersistedAt  time.Time
	lastPersistenceError error
}

type tokenRecord struct {
	TokenMeta
	Hash              string     `json:"hash"`
	PersistedLastSeen *time.Time `json:"-"`
}

type diskStore struct {
	Tokens []tokenRecord `json:"tokens"`
}

type diskInput struct {
	Tokens *[]tokenRecord `json:"tokens"`
}

type syncCloser interface {
	Sync() error
	Close() error
}

type atomicFile interface {
	io.Writer
	syncCloser
	Chmod(os.FileMode) error
	Name() string
}

type fileSystem interface {
	ReadFile(string) ([]byte, error)
	Stat(string) (os.FileInfo, error)
	MkdirAll(string, os.FileMode) error
	CreateTemp(string, string) (atomicFile, error)
	OpenDirectory(string) (syncCloser, error)
	Rename(string, string) error
}

type osFileSystem struct{}

func (osFileSystem) ReadFile(path string) ([]byte, error)  { return os.ReadFile(path) }
func (osFileSystem) Stat(path string) (os.FileInfo, error) { return os.Stat(path) }
func (osFileSystem) MkdirAll(path string, mode os.FileMode) error {
	return os.MkdirAll(path, mode)
}
func (osFileSystem) CreateTemp(dir, pattern string) (atomicFile, error) {
	return os.CreateTemp(dir, pattern)
}
func (osFileSystem) OpenDirectory(path string) (syncCloser, error) { return os.Open(path) }
func (osFileSystem) Rename(oldPath, newPath string) error          { return os.Rename(oldPath, newPath) }

type persistResult struct {
	committed bool
	err       error
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
		fs:      osFileSystem{},
		records: make(map[string]*tokenRecord),
	}
	for _, option := range opts {
		if option != nil {
			option(store)
		}
	}

	changed := false
	raw, err := store.fs.ReadFile(path)
	switch {
	case err == nil:
		var disk *diskInput
		decoder := json.NewDecoder(strings.NewReader(string(raw)))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&disk); err != nil {
			return nil, fmt.Errorf("decode token store: %w", err)
		}
		if disk == nil || disk.Tokens == nil {
			return nil, errors.New("decode token store: tokens must be a non-null array")
		}
		if decoder.Decode(&struct{}{}) != io.EOF {
			return nil, errors.New("decode token store: trailing JSON")
		}
		hashes := make(map[string]string, len(*disk.Tokens))
		for i := range *disk.Tokens {
			record := (*disk.Tokens)[i]
			if err := validateRecord(record); err != nil {
				return nil, fmt.Errorf("decode token store: %w", err)
			}
			if _, exists := store.records[record.ID]; exists {
				return nil, fmt.Errorf("decode token store: %w: %s", ErrDuplicateID, record.ID)
			}
			if existingID, exists := hashes[record.Hash]; exists {
				return nil, fmt.Errorf("decode token store: %w for %s and %s", ErrDuplicateHash, existingID, record.ID)
			}
			hashes[record.Hash] = record.ID
			record.LastSeenAt = copyTime(record.LastSeenAt)
			record.PersistedLastSeen = copyTime(record.LastSeenAt)
			if record.LastSeenAt != nil && record.LastSeenAt.After(store.lastSeenPersistedAt) {
				store.lastSeenPersistedAt = *record.LastSeenAt
			}
			store.records[record.ID] = &record
		}
		if info, statErr := store.fs.Stat(path); statErr != nil {
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
	if err := validateUniqueHashes(store.records); err != nil {
		return nil, fmt.Errorf("decode token store: %w", err)
	}
	if changed {
		if result := store.persistLocked(false); result.err != nil {
			return nil, result.err
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
		result := s.persistLocked(true)
		s.lastPersistenceError = result.err
		if result.committed {
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
	if err := validateUniqueHashes(s.records); err != nil {
		delete(s.records, meta.ID)
		return "", TokenMeta{}, err
	}
	result := s.persistLocked(false)
	s.lastPersistenceError = result.err
	if result.err != nil && !result.committed {
		delete(s.records, meta.ID)
		return "", TokenMeta{}, result.err
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
	result := s.persistLocked(false)
	s.lastPersistenceError = result.err
	if result.err != nil && !result.committed {
		s.records[id] = record
		return result.err
	}
	return nil
}

func (s *Store) persistLocked(flushLastSeen bool) persistResult {
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
		return persistResult{err: fmt.Errorf("encode token store: %w", err)}
	}
	raw = append(raw, '\n')

	dir := filepath.Dir(s.path)
	if err := s.fs.MkdirAll(dir, 0o700); err != nil {
		return persistResult{err: fmt.Errorf("create token store directory: %w", err)}
	}
	directory, err := s.fs.OpenDirectory(dir)
	if err != nil {
		return persistResult{err: fmt.Errorf("open token store directory: %w", err)}
	}
	directoryOpen := true
	defer func() {
		if directoryOpen {
			_ = directory.Close()
		}
	}()
	temp, err := s.fs.CreateTemp(dir, ".tokens-*")
	if err != nil {
		return persistResult{err: fmt.Errorf("create temporary token store: %w", err)}
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
		return persistResult{err: fmt.Errorf("set temporary token store mode: %w", err)}
	}
	if _, err := temp.Write(raw); err != nil {
		return persistResult{err: fmt.Errorf("write temporary token store: %w", err)}
	}
	if err := temp.Sync(); err != nil {
		return persistResult{err: fmt.Errorf("sync temporary token store: %w", err)}
	}
	if err := temp.Close(); err != nil {
		return persistResult{err: fmt.Errorf("close temporary token store: %w", err)}
	}
	if err := s.fs.Rename(tempName, s.path); err != nil {
		return persistResult{err: fmt.Errorf("replace token store: %w", err)}
	}
	keep = true
	if err := directory.Sync(); err != nil {
		return persistResult{committed: true, err: fmt.Errorf("sync token store directory: %w", err)}
	}
	if err := directory.Close(); err != nil {
		directoryOpen = false
		return persistResult{committed: true, err: fmt.Errorf("close token store directory: %w", err)}
	}
	directoryOpen = false
	return persistResult{committed: true}
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
	if record.CreatedAt.Location() != time.UTC {
		return fmt.Errorf("token %q creation time is not UTC", record.ID)
	}
	if record.LastSeenAt != nil {
		if record.LastSeenAt.Location() != time.UTC {
			return fmt.Errorf("token %q last-seen time is not UTC", record.ID)
		}
		if record.LastSeenAt.Before(record.CreatedAt) {
			return fmt.Errorf("token %q last-seen time precedes creation", record.ID)
		}
	}
	if len(record.Hash) != sha256.Size*2 || record.Hash != strings.ToLower(record.Hash) {
		return fmt.Errorf("token %q has invalid hash", record.ID)
	}
	if _, err := hex.DecodeString(record.Hash); err != nil {
		return fmt.Errorf("token %q has invalid hash", record.ID)
	}
	return nil
}

func validateUniqueHashes(records map[string]*tokenRecord) error {
	seen := make(map[string]string, len(records))
	for id, record := range records {
		if existingID, exists := seen[record.Hash]; exists {
			return fmt.Errorf("%w for %s and %s", ErrDuplicateHash, existingID, id)
		}
		seen[record.Hash] = id
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
