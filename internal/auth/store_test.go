package auth

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestTokenIssuePersistsOnlyHashAtomically(t *testing.T) {
	now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	random := bytes.Repeat([]byte{0x7d}, 32)
	path := filepath.Join(t.TempDir(), "tokens.json")
	store, err := OpenStore(path, "bootstrap-secret", WithClock(func() time.Time { return now }), WithRandom(bytes.NewReader(random)))
	if err != nil {
		t.Fatal(err)
	}
	secret, meta, err := store.Issue("Keith's iPhone")
	if err != nil {
		t.Fatal(err)
	}
	wantSecret := "wlt_" + hex.EncodeToString(random)
	if secret != wantSecret || meta.ID != strings.Repeat("7d", 8) {
		t.Fatalf("Issue() = (%q, %#v), want (%q, ID %q)", secret, meta, wantSecret, strings.Repeat("7d", 8))
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(secret)) || bytes.Contains(raw, random) || bytes.Contains(raw, []byte("bootstrap-secret")) {
		t.Fatalf("token store contains plaintext secret: %s", raw)
	}
	wantHash := sha256.Sum256([]byte(secret))
	if !bytes.Contains(raw, []byte(hex.EncodeToString(wantHash[:]))) {
		t.Fatalf("token store does not contain lowercase SHA-256 hash: %s", raw)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("store mode = %v, %v; want 0600", info, err)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != filepath.Base(path) {
			t.Fatalf("atomic persistence left unexpected file %q", entry.Name())
		}
	}
}

func TestTokenBootstrapMetadataAndAuthentication(t *testing.T) {
	now := time.Date(2026, 7, 17, 19, 0, 0, 0, time.UTC)
	store, err := OpenStore(filepath.Join(t.TempDir(), "tokens.json"), "bootstrap-secret", WithClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	principal, ok := store.Authenticate("bootstrap-secret")
	if !ok || principal != (Principal{TokenID: "bootstrap", Role: RoleAdmin}) {
		t.Fatalf("Authenticate(bootstrap) = %#v, %v", principal, ok)
	}
	listed := store.List()
	if len(listed) != 1 || listed[0].ID != "bootstrap" || listed[0].Label != "Bootstrap administrator" || !listed[0].Bootstrap || !listed[0].CreatedAt.Equal(now) {
		t.Fatalf("List() = %#v", listed)
	}
	if err := store.Revoke("bootstrap"); err == nil {
		t.Fatal("Revoke(bootstrap) succeeded")
	}
}

func TestTokenManagedAuthenticationAndImmediateRevocation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	store, err := OpenStore(path, "bootstrap", WithRandom(bytes.NewReader(bytes.Repeat([]byte{0x42}, 32))))
	if err != nil {
		t.Fatal(err)
	}
	secret, meta, err := store.Issue("phone")
	if err != nil {
		t.Fatal(err)
	}
	principal, ok := store.Authenticate(secret)
	if !ok || principal != (Principal{TokenID: meta.ID, Role: RoleClient}) {
		t.Fatalf("Authenticate(managed) = %#v, %v", principal, ok)
	}
	if _, ok := store.Authenticate(secret + "x"); ok {
		t.Fatal("Authenticate accepted a different secret")
	}
	if err := store.Revoke(meta.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Authenticate(secret); ok {
		t.Fatal("revoked token remained usable")
	}
	reopened, err := OpenStore(path, "bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.Authenticate(secret); ok {
		t.Fatal("revoked token returned after reopening store")
	}
}

func TestTokenRejectsUnsafeLabels(t *testing.T) {
	tests := []string{"", "   ", " leading", "trailing ", "line\nbreak", strings.Repeat("x", 129), string([]byte{0xff})}
	for _, label := range tests {
		t.Run(strings.ReplaceAll(label, "\n", "newline"), func(t *testing.T) {
			store, err := OpenStore(filepath.Join(t.TempDir(), "tokens.json"), "bootstrap")
			if err != nil {
				t.Fatal(err)
			}
			if _, _, err := store.Issue(label); err == nil {
				t.Fatalf("Issue(%q) succeeded", label)
			}
		})
	}
}

func TestTokenRejectsDuplicateID(t *testing.T) {
	one := append(bytes.Repeat([]byte{0x11}, 8), bytes.Repeat([]byte{0x22}, 24)...)
	two := append(bytes.Repeat([]byte{0x11}, 8), bytes.Repeat([]byte{0x33}, 24)...)
	store, err := OpenStore(filepath.Join(t.TempDir(), "tokens.json"), "bootstrap", WithRandom(bytes.NewReader(append(one, two...))))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Issue("first"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Issue("second"); err == nil {
		t.Fatal("Issue accepted a duplicate token ID")
	}
}

func TestTokenCorruptFileFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	if err := os.WriteFile(path, []byte(`{"tokens":[`), 0o600); err != nil {
		t.Fatal(err)
	}
	if store, err := OpenStore(path, "bootstrap"); err == nil || store != nil {
		t.Fatalf("OpenStore(corrupt) = %#v, %v", store, err)
	}
}

func TestTokenOpenRepairsPermissiveStoreMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	if _, err := OpenStore(path, "bootstrap"); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenStore(path, "bootstrap"); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("store mode after reopen = %o, want 600", got)
	}
}

func TestTokenLastSeenPersistenceIsCoalesced(t *testing.T) {
	now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "tokens.json")
	store, err := OpenStore(path, "bootstrap", WithClock(func() time.Time { return now }), WithRandom(bytes.NewReader(bytes.Repeat([]byte{0xaa}, 32))))
	if err != nil {
		t.Fatal(err)
	}
	secret, meta, err := store.Issue("phone")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Authenticate(secret); !ok {
		t.Fatal("initial authentication failed")
	}
	first := readDiskLastSeen(t, path, meta.ID)
	now = now.Add(30 * time.Minute)
	if _, ok := store.Authenticate(secret); !ok {
		t.Fatal("second authentication failed")
	}
	if got := readDiskLastSeen(t, path, meta.ID); !got.Equal(first) {
		t.Fatalf("disk last seen changed within coalescing window: %v -> %v", first, got)
	}
	listed := store.List()
	if listed[1].LastSeenAt == nil || !listed[1].LastSeenAt.Equal(now) {
		t.Fatalf("in-memory LastSeenAt = %v, want %v", listed[1].LastSeenAt, now)
	}
	now = now.Add(31 * time.Minute)
	if _, ok := store.Authenticate(secret); !ok {
		t.Fatal("third authentication failed")
	}
	if got := readDiskLastSeen(t, path, meta.ID); !got.Equal(now) {
		t.Fatalf("disk last seen = %v, want %v", got, now)
	}
}

func TestTokenLastSeenPersistenceIsCoalescedAcrossTokens(t *testing.T) {
	now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "tokens.json")
	random := append(bytes.Repeat([]byte{0xaa}, 32), bytes.Repeat([]byte{0xbb}, 32)...)
	store, err := OpenStore(path, "bootstrap", WithClock(func() time.Time { return now }), WithRandom(bytes.NewReader(random)))
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := store.Issue("first")
	if err != nil {
		t.Fatal(err)
	}
	second, secondMeta, err := store.Issue("second")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Authenticate(first); !ok {
		t.Fatal("first authentication failed")
	}
	now = now.Add(30 * time.Minute)
	if _, ok := store.Authenticate(second); !ok {
		t.Fatal("second authentication failed")
	}
	if got := findDiskLastSeen(t, path, secondMeta.ID); got != nil {
		t.Fatalf("second token was persisted inside global coalescing window: %v", got)
	}
}

func TestTokenMetadataWritesDoNotFlushUncoalescedLastSeen(t *testing.T) {
	now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "tokens.json")
	random := append(bytes.Repeat([]byte{0xaa}, 32), bytes.Repeat([]byte{0xbb}, 32)...)
	store, err := OpenStore(path, "bootstrap", WithClock(func() time.Time { return now }), WithRandom(bytes.NewReader(random)))
	if err != nil {
		t.Fatal(err)
	}
	secret, meta, err := store.Issue("first")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Authenticate(secret); !ok {
		t.Fatal("first authentication failed")
	}
	first := readDiskLastSeen(t, path, meta.ID)
	now = now.Add(30 * time.Minute)
	if _, ok := store.Authenticate(secret); !ok {
		t.Fatal("second authentication failed")
	}
	if _, _, err := store.Issue("second"); err != nil {
		t.Fatal(err)
	}
	if got := readDiskLastSeen(t, path, meta.ID); !got.Equal(first) {
		t.Fatalf("metadata write flushed last seen inside coalescing window: %v -> %v", first, got)
	}
}

func TestTokenConcurrentAccessReturnsSafeCopies(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "tokens.json"), "bootstrap", WithRandom(bytes.NewReader(bytes.Repeat([]byte{0xbb}, 32))))
	if err != nil {
		t.Fatal(err)
	}
	secret, _, err := store.Issue("phone")
	if err != nil {
		t.Fatal(err)
	}
	listed := store.List()
	listed[0].Label = "mutated"
	if store.List()[0].Label != "Bootstrap administrator" {
		t.Fatal("List returned mutable internal metadata")
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			store.Authenticate(secret)
			_ = store.List()
		}()
	}
	wg.Wait()
}

func readDiskLastSeen(t *testing.T, path, id string) time.Time {
	t.Helper()
	value := findDiskLastSeen(t, path, id)
	if value == nil {
		t.Fatalf("token %q has nil disk last seen", id)
	}
	return *value
}

func findDiskLastSeen(t *testing.T, path, id string) *time.Time {
	t.Helper()
	var disk struct {
		Tokens []struct {
			ID         string     `json:"id"`
			LastSeenAt *time.Time `json:"last_seen_at"`
		} `json:"tokens"`
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &disk); err != nil {
		t.Fatal(err)
	}
	for _, token := range disk.Tokens {
		if token.ID == id {
			return token.LastSeenAt
		}
	}
	t.Fatalf("token %q missing from disk", id)
	return nil
}
