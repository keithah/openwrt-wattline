package auth

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	store, err := OpenStore(path, "bootstrap-secret", withClock(func() time.Time { return now }), withRandom(bytes.NewReader(random)))
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
	store, err := OpenStore(filepath.Join(t.TempDir(), "tokens.json"), "bootstrap-secret", withClock(func() time.Time { return now }))
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
	store, err := OpenStore(path, "bootstrap", withRandom(bytes.NewReader(bytes.Repeat([]byte{0x42}, 32))))
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
	store, err := OpenStore(filepath.Join(t.TempDir(), "tokens.json"), "bootstrap", withRandom(bytes.NewReader(append(one, two...))))
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

func TestTokenIssueRejectsDuplicateHash(t *testing.T) {
	random := bytes.Repeat([]byte{0x77}, 32)
	bootstrap := "wlt_" + hex.EncodeToString(random)
	store, err := OpenStore(filepath.Join(t.TempDir(), "tokens.json"), bootstrap, withRandom(bytes.NewReader(random)))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.Issue("same secret"); err == nil {
		t.Fatal("Issue accepted a secret hash already owned by bootstrap")
	}
	if got := store.List(); len(got) != 1 || !got[0].Bootstrap {
		t.Fatalf("List after duplicate hash = %#v", got)
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

func TestTokenMissingOrNullDocumentFailsClosed(t *testing.T) {
	for _, raw := range []string{"null", `{}`, `{"tokens":null}`} {
		t.Run(raw, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tokens.json")
			if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
				t.Fatal(err)
			}
			if store, err := OpenStore(path, "bootstrap"); err == nil || store != nil {
				t.Fatalf("OpenStore(%s) = %#v, %v", raw, store, err)
			}
		})
	}
}

func TestTokenRejectsDuplicateHashes(t *testing.T) {
	now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	duplicate := hashSecret("same-secret")
	tests := []struct {
		name      string
		bootstrap string
		tokens    []tokenRecord
	}{
		{
			name:      "managed tokens",
			bootstrap: "bootstrap",
			tokens: []tokenRecord{
				{TokenMeta: TokenMeta{ID: "1111111111111111", Label: "one", CreatedAt: now}, Hash: duplicate},
				{TokenMeta: TokenMeta{ID: "2222222222222222", Label: "two", CreatedAt: now}, Hash: duplicate},
			},
		},
		{
			name:      "bootstrap reconciliation",
			bootstrap: "new-bootstrap",
			tokens: []tokenRecord{
				{TokenMeta: TokenMeta{ID: bootstrapID, Label: bootstrapLabel, CreatedAt: now, Bootstrap: true}, Hash: hashSecret("old-bootstrap")},
				{TokenMeta: TokenMeta{ID: "3333333333333333", Label: "client", CreatedAt: now}, Hash: hashSecret("new-bootstrap")},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tokens.json")
			writeDiskTokens(t, path, test.tokens)
			if store, err := OpenStore(path, test.bootstrap); err == nil || store != nil {
				t.Fatalf("OpenStore(duplicate hashes) = %#v, %v", store, err)
			}
		})
	}
}

func TestTokenRejectsInvalidPersistedTimestamps(t *testing.T) {
	utc := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		created time.Time
		seen    *time.Time
	}{
		{name: "non UTC creation", created: time.Date(2026, 7, 17, 13, 0, 0, 0, time.FixedZone("PDT", -7*60*60))},
		{name: "non UTC last seen", created: utc, seen: timePointer(time.Date(2026, 7, 17, 13, 1, 0, 0, time.FixedZone("PDT", -7*60*60)))},
		{name: "last seen before creation", created: utc, seen: timePointer(utc.Add(-time.Second))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "tokens.json")
			writeDiskTokens(t, path, []tokenRecord{{
				TokenMeta: TokenMeta{ID: "1111111111111111", Label: "client", CreatedAt: test.created, LastSeenAt: test.seen},
				Hash:      hashSecret("secret"),
			}})
			if store, err := OpenStore(path, "bootstrap"); err == nil || store != nil {
				t.Fatalf("OpenStore(invalid timestamp) = %#v, %v", store, err)
			}
		})
	}
}

func TestTokenIssueRollsBackOnlyBeforeAtomicCommit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	store, err := OpenStore(path, "bootstrap", withRandom(bytes.NewReader(bytes.Repeat([]byte{0x44}, 32))))
	if err != nil {
		t.Fatal(err)
	}
	faults := &faultFileSystem{fileSystem: osFileSystem{}, renameErr: errors.New("rename failed")}
	store.fs = faults
	secret, meta, err := store.Issue("phone")
	if err == nil || secret != "" || meta != (TokenMeta{}) {
		t.Fatalf("Issue before commit = (%q, %#v, %v)", secret, meta, err)
	}
	if got := store.List(); len(got) != 1 {
		t.Fatalf("List after pre-commit failure = %#v", got)
	}
}

func TestTokenIssueSurvivesPostCommitDirectorySyncFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	store, err := OpenStore(path, "bootstrap", withRandom(bytes.NewReader(bytes.Repeat([]byte{0x55}, 32))))
	if err != nil {
		t.Fatal(err)
	}
	faults := &faultFileSystem{fileSystem: osFileSystem{}, directorySyncErr: errors.New("directory sync failed")}
	store.fs = faults
	secret, meta, err := store.Issue("phone")
	if err != nil || secret == "" || meta.ID == "" {
		t.Fatalf("Issue after committed rename = (%q, %#v, %v)", secret, meta, err)
	}
	if store.lastPersistenceError == nil {
		t.Fatal("post-commit directory sync failure was not recorded")
	}
	if principal, ok := store.Authenticate(secret); !ok || principal.Role != RoleClient {
		t.Fatalf("committed issued token does not authenticate: %#v, %v", principal, ok)
	}
	reopened, err := OpenStore(path, "bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.Authenticate(secret); !ok {
		t.Fatal("committed issued token was absent after reopen")
	}
}

func TestTokenRevokeSurvivesPostCommitDirectorySyncFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	store, err := OpenStore(path, "bootstrap", withRandom(bytes.NewReader(bytes.Repeat([]byte{0x66}, 32))))
	if err != nil {
		t.Fatal(err)
	}
	secret, meta, err := store.Issue("phone")
	if err != nil {
		t.Fatal(err)
	}
	revoked, cancel, active := store.SubscribeRevocation(meta.ID)
	defer cancel()
	if !active {
		t.Fatal("issued token subscription was inactive")
	}
	store.fs = &faultFileSystem{fileSystem: osFileSystem{}, directorySyncErr: errors.New("directory sync failed")}
	if err := store.Revoke(meta.ID); err != nil {
		t.Fatalf("Revoke after committed rename: %v", err)
	}
	if store.lastPersistenceError == nil {
		t.Fatal("post-commit directory sync failure was not recorded")
	}
	if _, ok := store.Authenticate(secret); ok {
		t.Fatal("committed revocation was rolled back in memory")
	}
	select {
	case <-revoked:
	default:
		t.Fatal("committed revocation did not notify subscriber")
	}
	reopened, err := OpenStore(path, "bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.Authenticate(secret); ok {
		t.Fatal("committed revocation was absent after reopen")
	}
}

func TestTokenRevocationSubscriptionWaitsForCommitAndCanCancel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	random := append(bytes.Repeat([]byte{0x67}, 32), bytes.Repeat([]byte{0x68}, 32)...)
	store, err := OpenStore(path, "bootstrap", withRandom(bytes.NewReader(random)))
	if err != nil {
		t.Fatal(err)
	}
	first, firstMeta, err := store.Issue("first")
	if err != nil {
		t.Fatal(err)
	}
	_, secondMeta, err := store.Issue("second")
	if err != nil {
		t.Fatal(err)
	}

	firstRevoked, cancelFirst, active := store.SubscribeRevocation(firstMeta.ID)
	defer cancelFirst()
	if !active {
		t.Fatal("first issued token subscription was inactive")
	}
	store.fs = &faultFileSystem{fileSystem: osFileSystem{}, renameErr: errors.New("rename failed")}
	if err := store.Revoke(firstMeta.ID); err == nil {
		t.Fatal("Revoke succeeded before atomic commit")
	}
	select {
	case <-firstRevoked:
		t.Fatal("failed revocation notified subscriber")
	default:
	}
	if _, ok := store.Authenticate(first); !ok {
		t.Fatal("failed revocation removed token")
	}

	store.fs = osFileSystem{}
	secondRevoked, cancelSecond, active := store.SubscribeRevocation(secondMeta.ID)
	if !active {
		t.Fatal("second issued token subscription was inactive")
	}
	cancelSecond()
	if err := store.Revoke(secondMeta.ID); err != nil {
		t.Fatal(err)
	}
	select {
	case <-secondRevoked:
		t.Fatal("canceled revocation subscriber was notified")
	default:
	}

	missing, cancelMissing, active := store.SubscribeRevocation("9999999999999999")
	defer cancelMissing()
	if active || missing != nil {
		t.Fatal("missing-token subscription was active")
	}
	bootstrap, cancelBootstrap, active := store.SubscribeRevocation("bootstrap")
	defer cancelBootstrap()
	if !active || bootstrap != nil {
		t.Fatal("bootstrap subscription state was incorrect")
	}
}

func TestManagedSubscriberInvalidationRejectsFutureSubscriptionsWithoutRevoking(t *testing.T) {
	store, err := OpenStore(filepath.Join(t.TempDir(), "tokens.json"), "bootstrap")
	if err != nil {
		t.Fatal(err)
	}
	secret, meta, err := store.Issue("phone")
	if err != nil {
		t.Fatal(err)
	}
	store.InvalidateManagedSubscribers()
	ch, cancel, active := store.SubscribeRevocation(meta.ID)
	defer cancel()
	if active || ch != nil {
		t.Fatal("retired store accepted a new managed subscription")
	}
	if _, ok := store.Authenticate(secret); !ok {
		t.Fatal("subscriber invalidation revoked the managed token")
	}
	bootstrap, cancelBootstrap, active := store.SubscribeRevocation("bootstrap")
	defer cancelBootstrap()
	if !active || bootstrap != nil {
		t.Fatal("subscriber invalidation affected bootstrap")
	}
}

func TestTokenAuthenticateDoesNotRetryAfterCommittedSyncFailure(t *testing.T) {
	now := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	path := filepath.Join(t.TempDir(), "tokens.json")
	store, err := OpenStore(path, "bootstrap", withClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	faults := &faultFileSystem{fileSystem: osFileSystem{}, directorySyncErr: errors.New("directory sync failed")}
	store.fs = faults
	if _, ok := store.Authenticate("bootstrap"); !ok {
		t.Fatal("first authentication failed")
	}
	now = now.Add(time.Minute)
	if _, ok := store.Authenticate("bootstrap"); !ok {
		t.Fatal("second authentication failed")
	}
	if faults.renameCalls != 1 {
		t.Fatalf("Authenticate rename calls = %d, want 1", faults.renameCalls)
	}
}

func TestTokenAuthenticateClampsClockRollbackToCreation(t *testing.T) {
	created := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	now := created
	path := filepath.Join(t.TempDir(), "tokens.json")
	store, err := OpenStore(path, "bootstrap", withClock(func() time.Time { return now }), withRandom(bytes.NewReader(bytes.Repeat([]byte{0x88}, 32))))
	if err != nil {
		t.Fatal(err)
	}
	secret, meta, err := store.Issue("phone")
	if err != nil {
		t.Fatal(err)
	}
	now = created.Add(-time.Hour)
	if _, ok := store.Authenticate(secret); !ok {
		t.Fatal("authentication failed after clock rollback")
	}
	listed := store.List()
	if listed[1].LastSeenAt == nil || !listed[1].LastSeenAt.Equal(meta.CreatedAt) {
		t.Fatalf("LastSeenAt after rollback = %v, want creation %v", listed[1].LastSeenAt, meta.CreatedAt)
	}
	if _, err := OpenStore(path, "bootstrap"); err != nil {
		t.Fatalf("reopen after rolled-back authentication: %v", err)
	}
}

func TestTokenAuthenticatePreservesLaterLastSeenOnClockRollback(t *testing.T) {
	created := time.Date(2026, 7, 17, 20, 0, 0, 0, time.UTC)
	now := created
	path := filepath.Join(t.TempDir(), "tokens.json")
	store, err := OpenStore(path, "bootstrap", withClock(func() time.Time { return now }), withRandom(bytes.NewReader(bytes.Repeat([]byte{0x99}, 32))))
	if err != nil {
		t.Fatal(err)
	}
	secret, meta, err := store.Issue("phone")
	if err != nil {
		t.Fatal(err)
	}
	later := created.Add(2 * time.Hour)
	now = later
	if _, ok := store.Authenticate(secret); !ok {
		t.Fatal("initial authentication failed")
	}
	faults := &faultFileSystem{fileSystem: osFileSystem{}}
	store.fs = faults
	now = created.Add(time.Hour)
	if _, ok := store.Authenticate(secret); !ok {
		t.Fatal("authentication failed after clock rollback")
	}
	listed := store.List()
	if listed[1].ID != meta.ID || listed[1].LastSeenAt == nil || !listed[1].LastSeenAt.Equal(later) {
		t.Fatalf("LastSeenAt after rollback = %#v, want %v", listed[1], later)
	}
	if faults.renameCalls != 0 {
		t.Fatalf("clock rollback triggered %d persistence writes", faults.renameCalls)
	}
	reopened, err := OpenStore(path, "bootstrap")
	if err != nil {
		t.Fatalf("reopen after preserving later last seen: %v", err)
	}
	reopenedMeta := reopened.List()
	if reopenedMeta[1].LastSeenAt == nil || !reopenedMeta[1].LastSeenAt.Equal(later) {
		t.Fatalf("persisted LastSeenAt = %v, want %v", reopenedMeta[1].LastSeenAt, later)
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
	store, err := OpenStore(path, "bootstrap", withClock(func() time.Time { return now }), withRandom(bytes.NewReader(bytes.Repeat([]byte{0xaa}, 32))))
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
	store, err := OpenStore(path, "bootstrap", withClock(func() time.Time { return now }), withRandom(bytes.NewReader(random)))
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
	store, err := OpenStore(path, "bootstrap", withClock(func() time.Time { return now }), withRandom(bytes.NewReader(random)))
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
	store, err := OpenStore(filepath.Join(t.TempDir(), "tokens.json"), "bootstrap", withRandom(bytes.NewReader(bytes.Repeat([]byte{0xbb}, 32))))
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

func writeDiskTokens(t *testing.T, path string, tokens []tokenRecord) {
	t.Helper()
	raw, err := json.Marshal(diskStore{Tokens: tokens})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func timePointer(value time.Time) *time.Time { return &value }

type faultFileSystem struct {
	fileSystem
	renameErr        error
	directorySyncErr error
	renameCalls      int
}

func (f *faultFileSystem) Rename(oldPath, newPath string) error {
	f.renameCalls++
	if f.renameErr != nil {
		return f.renameErr
	}
	return f.fileSystem.Rename(oldPath, newPath)
}

func (f *faultFileSystem) OpenDirectory(path string) (syncCloser, error) {
	directory, err := f.fileSystem.OpenDirectory(path)
	if err != nil {
		return nil, err
	}
	return &faultDirectory{syncCloser: directory, syncErr: f.directorySyncErr}, nil
}

type faultDirectory struct {
	syncCloser
	syncErr error
}

func (d *faultDirectory) Sync() error {
	if d.syncErr != nil {
		return d.syncErr
	}
	return d.syncCloser.Sync()
}
