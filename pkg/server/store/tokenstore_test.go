package store

import (
	"context"
	"database/sql"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func setupTestTokenDB(t *testing.T) *sql.DB {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}

	if err := InitTokenSchema(db); err != nil {
		db.Close()
		t.Fatalf("failed to init token schema: %v", err)
	}

	return db
}

func TestTokenStoreCreateAndGet(t *testing.T) {
	db := setupTestTokenDB(t)
	defer db.Close()

	store := NewSQLiteTokenStore(db)
	ctx := context.Background()

	token := &TokenRecord{
		Token:     "test-token-123",
		CreatedAt: time.Now(),
		MaxUses:   1,
	}

	err := store.CreateToken(ctx, token)
	if err != nil {
		t.Fatalf("CreateToken failed: %v", err)
	}

	retrieved, err := store.GetToken(ctx, "test-token-123")
	if err != nil {
		t.Fatalf("GetToken failed: %v", err)
	}

	if retrieved == nil {
		t.Fatal("GetToken returned nil")
	}

	if retrieved.Token != token.Token {
		t.Errorf("expected token %s, got %s", token.Token, retrieved.Token)
	}
}

func TestTokenStoreUse(t *testing.T) {
	db := setupTestTokenDB(t)
	defer db.Close()

	store := NewSQLiteTokenStore(db)
	ctx := context.Background()

	token := &TokenRecord{
		Token:     "use-token-456",
		CreatedAt: time.Now(),
		MaxUses:   1,
	}

	err := store.CreateToken(ctx, token)
	if err != nil {
		t.Fatalf("CreateToken failed: %v", err)
	}

	used, err := store.UseToken(ctx, "use-token-456", "")
	if err != nil {
		t.Fatalf("UseToken failed: %v", err)
	}

	if used == nil {
		t.Fatal("UseToken returned nil")
	}

	if used.Uses != 1 {
		t.Errorf("expected uses=1, got %d", used.Uses)
	}

	if used.UsedAt == nil {
		t.Error("UsedAt should be set")
	}
}

func TestTokenStoreMaxUses(t *testing.T) {
	db := setupTestTokenDB(t)
	defer db.Close()

	store := NewSQLiteTokenStore(db)
	ctx := context.Background()

	token := &TokenRecord{
		Token:     "multi-use-token",
		CreatedAt: time.Now(),
		MaxUses:   2,
	}

	err := store.CreateToken(ctx, token)
	if err != nil {
		t.Fatalf("CreateToken failed: %v", err)
	}

	// Use twice
	_, err = store.UseToken(ctx, "multi-use-token", "")
	if err != nil {
		t.Fatalf("First UseToken failed: %v", err)
	}

	_, err = store.UseToken(ctx, "multi-use-token", "")
	if err != nil {
		t.Fatalf("Second UseToken failed: %v", err)
	}

	// Third use should fail
	_, err = store.UseToken(ctx, "multi-use-token", "")
	if err == nil {
		t.Error("Third use should have failed")
	}
}

func TestTokenStoreExpired(t *testing.T) {
	db := setupTestTokenDB(t)
	defer db.Close()

	store := NewSQLiteTokenStore(db)
	ctx := context.Background()

	// Token that expired yesterday
	past := time.Now().Add(-24 * time.Hour)
	token := &TokenRecord{
		Token:     "expired-token",
		CreatedAt: past.Add(-time.Hour),
		ExpiresAt: &past,
		MaxUses:   1,
	}

	err := store.CreateToken(ctx, token)
	if err != nil {
		t.Fatalf("CreateToken failed: %v", err)
	}

	_, err = store.UseToken(ctx, "expired-token", "")
	if err == nil {
		t.Error("Should have failed for expired token")
	}
}

func TestTokenStoreDelete(t *testing.T) {
	db := setupTestTokenDB(t)
	defer db.Close()

	store := NewSQLiteTokenStore(db)
	ctx := context.Background()

	token := &TokenRecord{
		Token:     "delete-me",
		CreatedAt: time.Now(),
		MaxUses:   1,
	}

	err := store.CreateToken(ctx, token)
	if err != nil {
		t.Fatalf("CreateToken failed: %v", err)
	}

	err = store.DeleteToken(ctx, "delete-me")
	if err != nil {
		t.Fatalf("DeleteToken failed: %v", err)
	}

	retrieved, err := store.GetToken(ctx, "delete-me")
	if err != nil {
		t.Fatalf("GetToken failed: %v", err)
	}
	if retrieved != nil {
		t.Error("Token should be deleted")
	}
}

func TestTokenStoreList(t *testing.T) {
	db := setupTestTokenDB(t)
	defer db.Close()

	store := NewSQLiteTokenStore(db)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		token := &TokenRecord{
			Token:     "list-token-" + string(rune('a'+i)),
			CreatedAt: time.Now().Add(-time.Duration(i) * time.Hour),
			MaxUses:   1,
		}
		err := store.CreateToken(ctx, token)
		if err != nil {
			t.Fatalf("CreateToken failed: %v", err)
		}
	}

	tokens, err := store.ListTokens(ctx)
	if err != nil {
		t.Fatalf("ListTokens failed: %v", err)
	}

	if len(tokens) != 3 {
		t.Errorf("expected 3 tokens, got %d", len(tokens))
	}
}

func TestTokenStoreGetNotFound(t *testing.T) {
	db := setupTestTokenDB(t)
	defer db.Close()

	store := NewSQLiteTokenStore(db)
	ctx := context.Background()

	retrieved, err := store.GetToken(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetToken should not error for nonexistent: %v", err)
	}
	if retrieved != nil {
		t.Error("GetToken should return nil for nonexistent")
	}
}

// setupTestTokenDBOnFile returns a token database backed by a file, so several
// connections of the pool see the same rows (":memory:" would give each
// connection its own empty database and hide the concurrency under test).
func setupTestTokenDBOnFile(t *testing.T) *sql.DB {
	t.Helper()

	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "tokens.db"))
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	if err := InitTokenSchema(db); err != nil {
		db.Close()
		t.Fatalf("failed to init token schema: %v", err)
	}
	return db
}

// A single-use token must admit exactly one consumer, even when many callers
// race on it, and the stored counter must end at 1.
func TestTokenStoreUseTokenConcurrentSingleUse(t *testing.T) {
	const workers = 32

	db := setupTestTokenDBOnFile(t)
	defer db.Close()

	store := NewSQLiteTokenStore(db)
	ctx := context.Background()

	if err := store.CreateToken(ctx, &TokenRecord{
		Token:     "race-token",
		CreatedAt: time.Now(),
		MaxUses:   1,
	}); err != nil {
		t.Fatalf("CreateToken failed: %v", err)
	}

	start := make(chan struct{})
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = store.UseToken(ctx, "race-token", "")
		}(i)
	}
	close(start)
	wg.Wait()

	ok, exceeded := 0, 0
	for i, err := range errs {
		switch {
		case err == nil:
			ok++
		case err.Error() == "token max uses exceeded":
			exceeded++
		default:
			t.Errorf("goroutine %d: unexpected error: %v", i, err)
		}
	}

	if ok != 1 {
		t.Errorf("expected exactly 1 successful use, got %d", ok)
	}
	if exceeded != workers-1 {
		t.Errorf("expected %d 'token max uses exceeded' errors, got %d", workers-1, exceeded)
	}

	rec, err := store.GetToken(ctx, "race-token")
	if err != nil {
		t.Fatalf("GetToken failed: %v", err)
	}
	if rec.Uses != 1 {
		t.Errorf("expected uses=1 after the race, got %d", rec.Uses)
	}
}

// max_uses = 0 keeps its previous, effective behaviour: the token can never be
// consumed, the row is left untouched and the message does not change.
func TestTokenStoreUseTokenMaxUsesZeroIsRejected(t *testing.T) {
	db := setupTestTokenDBOnFile(t)
	defer db.Close()

	store := NewSQLiteTokenStore(db)
	ctx := context.Background()

	if err := store.CreateToken(ctx, &TokenRecord{
		Token:     "zero-use-token",
		CreatedAt: time.Now(),
		MaxUses:   0,
	}); err != nil {
		t.Fatalf("CreateToken failed: %v", err)
	}

	for attempt := 1; attempt <= 3; attempt++ {
		_, err := store.UseToken(ctx, "zero-use-token", "")
		if err == nil {
			t.Fatalf("attempt %d: a max_uses=0 token must not be consumable", attempt)
		}
		if err.Error() != "token max uses exceeded" {
			t.Fatalf("attempt %d: expected 'token max uses exceeded', got %q", attempt, err.Error())
		}
	}

	rec, err := store.GetToken(ctx, "zero-use-token")
	if err != nil {
		t.Fatalf("GetToken failed: %v", err)
	}
	if rec.Uses != 0 {
		t.Errorf("expected uses=0, got %d", rec.Uses)
	}
	if rec.UsedAt != nil {
		t.Errorf("expected used_at to stay NULL, got %v", rec.UsedAt)
	}
}

// An expired token is rejected with the same message and is not consumed, even
// when many callers race on it.
func TestTokenStoreUseTokenExpiredConcurrent(t *testing.T) {
	const workers = 20

	db := setupTestTokenDBOnFile(t)
	defer db.Close()

	store := NewSQLiteTokenStore(db)
	ctx := context.Background()

	past := time.Now().Add(-time.Hour)
	if err := store.CreateToken(ctx, &TokenRecord{
		Token:     "expired-race-token",
		CreatedAt: past.Add(-time.Hour),
		ExpiresAt: &past,
		MaxUses:   1,
	}); err != nil {
		t.Fatalf("CreateToken failed: %v", err)
	}

	start := make(chan struct{})
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			_, errs[i] = store.UseToken(ctx, "expired-race-token", "")
		}(i)
	}
	close(start)
	wg.Wait()

	for i, err := range errs {
		if err == nil {
			t.Errorf("goroutine %d: an expired token must not be consumable", i)
			continue
		}
		if err.Error() != "token expired" {
			t.Errorf("goroutine %d: expected 'token expired', got %q", i, err.Error())
		}
	}

	rec, err := store.GetToken(ctx, "expired-race-token")
	if err != nil {
		t.Fatalf("GetToken failed: %v", err)
	}
	if rec.Uses != 0 {
		t.Errorf("expected uses=0, got %d", rec.Uses)
	}
}

// No expiry date (NULL expires_at) still means "never expires".
func TestTokenStoreUseTokenWithoutExpiryIsUsable(t *testing.T) {
	db := setupTestTokenDBOnFile(t)
	defer db.Close()

	store := NewSQLiteTokenStore(db)
	ctx := context.Background()

	if err := store.CreateToken(ctx, &TokenRecord{
		Token:     "no-expiry-token",
		CreatedAt: time.Now(),
		MaxUses:   2,
	}); err != nil {
		t.Fatalf("CreateToken failed: %v", err)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		if _, err := store.UseToken(ctx, "no-expiry-token", ""); err != nil {
			t.Fatalf("attempt %d: a token without expiry must be usable: %v", attempt, err)
		}
	}

	_, err := store.UseToken(ctx, "no-expiry-token", "")
	if err == nil || err.Error() != "token max uses exceeded" {
		t.Fatalf("third use: expected 'token max uses exceeded', got %v", err)
	}
}

// The consume statement records the node that used the token, so the audit
// trail survives revoking it.
func TestTokenStoreUseTokenRecordsUsedBy(t *testing.T) {
	db := setupTestTokenDB(t)
	defer db.Close()

	st := NewSQLiteTokenStore(db)
	ctx := context.Background()

	if err := st.CreateToken(ctx, &TokenRecord{Token: "used-by-token", CreatedAt: time.Now(), MaxUses: 2}); err != nil {
		t.Fatalf("CreateToken failed: %v", err)
	}

	used, err := st.UseToken(ctx, "used-by-token", "node-7")
	if err != nil {
		t.Fatalf("UseToken failed: %v", err)
	}
	if used.UsedBy == nil || *used.UsedBy != "node-7" {
		t.Errorf("used_by should be node-7, got %v", used.UsedBy)
	}

	stored, err := st.GetToken(ctx, "used-by-token")
	if err != nil {
		t.Fatalf("GetToken failed: %v", err)
	}
	if stored.UsedBy == nil || *stored.UsedBy != "node-7" {
		t.Errorf("stored used_by should be node-7, got %v", stored.UsedBy)
	}

	// A caller without a node keeps the recorded one instead of clearing it.
	if _, err := st.UseToken(ctx, "used-by-token", ""); err != nil {
		t.Fatalf("second UseToken failed: %v", err)
	}
	stored, err = st.GetToken(ctx, "used-by-token")
	if err != nil {
		t.Fatalf("GetToken failed: %v", err)
	}
	if stored.UsedBy == nil || *stored.UsedBy != "node-7" {
		t.Errorf("an empty node must not clear used_by, got %v", stored.UsedBy)
	}
	if stored.Uses != 2 {
		t.Errorf("uses should be 2, got %d", stored.Uses)
	}
}

// Revoking marks the row instead of consuming it: the token becomes unusable
// and the usage trail stays readable for audit.
func TestTokenStoreRevokeToken(t *testing.T) {
	db := setupTestTokenDB(t)
	defer db.Close()

	st := NewSQLiteTokenStore(db)
	ctx := context.Background()

	if err := st.CreateToken(ctx, &TokenRecord{Token: "revoke-me", CreatedAt: time.Now(), MaxUses: 5}); err != nil {
		t.Fatalf("CreateToken failed: %v", err)
	}
	if _, err := st.UseToken(ctx, "revoke-me", "node-a"); err != nil {
		t.Fatalf("UseToken failed: %v", err)
	}

	revoked, err := st.RevokeToken(ctx, "revoke-me")
	if err != nil {
		t.Fatalf("RevokeToken failed: %v", err)
	}
	if revoked.RevokedAt == nil {
		t.Fatal("RevokeToken must set revoked_at")
	}
	if revoked.Uses != 1 {
		t.Errorf("revoking must not consume a use, uses=%d (expected 1)", revoked.Uses)
	}
	if revoked.UsedBy == nil || *revoked.UsedBy != "node-a" {
		t.Errorf("revoking must keep used_by, got %v", revoked.UsedBy)
	}
	firstRevocation := *revoked.RevokedAt

	// A revoked token can never be consumed again, and the error says why.
	if _, err := st.UseToken(ctx, "revoke-me", "node-b"); err == nil {
		t.Fatal("a revoked token must not be usable")
	} else if err.Error() != "token revoked" {
		t.Errorf("expected the 'token revoked' error, got %v", err)
	}

	stored, err := st.GetToken(ctx, "revoke-me")
	if err != nil {
		t.Fatalf("GetToken failed: %v", err)
	}
	if stored.Uses != 1 {
		t.Errorf("a rejected consume must not change uses, got %d", stored.Uses)
	}
	if stored.UsedBy == nil || *stored.UsedBy != "node-a" {
		t.Errorf("a rejected consume must not change used_by, got %v", stored.UsedBy)
	}

	// Revoking twice keeps the first date; the sleep makes the comparison
	// meaningful because revocation is stored with second resolution.
	time.Sleep(1100 * time.Millisecond)
	again, err := st.RevokeToken(ctx, "revoke-me")
	if err != nil {
		t.Fatalf("a second RevokeToken failed: %v", err)
	}
	if again.RevokedAt == nil || !again.RevokedAt.Equal(firstRevocation) {
		t.Errorf("a second revocation must keep the first date: %v vs %v", again.RevokedAt, firstRevocation)
	}

	// Revoking a token that does not exist is reported, never silently ignored.
	if _, err := st.RevokeToken(ctx, "ghost"); err == nil {
		t.Fatal("revoking a missing token must fail")
	} else if err.Error() != "token not found" {
		t.Errorf("expected 'token not found', got %v", err)
	}
}

// Revoking works on an already exhausted token, which the old "consume a use"
// trick could not do (it failed with "token max uses exceeded").
func TestTokenStoreRevokeExhaustedToken(t *testing.T) {
	db := setupTestTokenDB(t)
	defer db.Close()

	st := NewSQLiteTokenStore(db)
	ctx := context.Background()

	if err := st.CreateToken(ctx, &TokenRecord{Token: "single-use", CreatedAt: time.Now(), MaxUses: 1}); err != nil {
		t.Fatalf("CreateToken failed: %v", err)
	}
	if _, err := st.UseToken(ctx, "single-use", "node-x"); err != nil {
		t.Fatalf("UseToken failed: %v", err)
	}
	if _, err := st.UseToken(ctx, "single-use", "node-y"); err == nil {
		t.Fatal("an exhausted token must not be usable")
	}

	revoked, err := st.RevokeToken(ctx, "single-use")
	if err != nil {
		t.Fatalf("revoking an exhausted token must work: %v", err)
	}
	if revoked.RevokedAt == nil {
		t.Error("an exhausted token must get revoked_at")
	}
	if revoked.UsedBy == nil || *revoked.UsedBy != "node-x" {
		t.Errorf("revoking must keep the first (and only) user, got %v", revoked.UsedBy)
	}
	if _, err := st.UseToken(ctx, "single-use", "node-z"); err == nil || err.Error() != "token revoked" {
		t.Errorf("an exhausted and revoked token must report 'token revoked', got %v", err)
	}
}

// A database created before revocation existed gains the column instead of
// failing every use: CREATE TABLE IF NOT EXISTS leaves old tables untouched.
func TestTokenStoreInitSchemaAddsRevokedAtToExistingDatabase(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	defer db.Close()

	legacy := `CREATE TABLE enrollment_tokens (
		token TEXT PRIMARY KEY,
		created_at INTEGER NOT NULL,
		expires_at INTEGER,
		used_at INTEGER,
		used_by TEXT,
		max_uses INTEGER NOT NULL DEFAULT 1,
		uses INTEGER NOT NULL DEFAULT 0
	);`
	if _, err := db.Exec(legacy); err != nil {
		t.Fatalf("failed to create the legacy table: %v", err)
	}
	if _, err := db.Exec(
		`INSERT INTO enrollment_tokens (token, created_at, max_uses, uses, used_by) VALUES (?, ?, ?, ?, ?)`,
		"legacy-token", time.Now().Unix(), 3, 1, "node-old"); err != nil {
		t.Fatalf("failed to seed the legacy row: %v", err)
	}

	if err := InitTokenSchema(db); err != nil {
		t.Fatalf("InitTokenSchema must migrate an existing database: %v", err)
	}
	// The migration must be idempotent.
	if err := InitTokenSchema(db); err != nil {
		t.Fatalf("a second InitTokenSchema failed: %v", err)
	}

	st := NewSQLiteTokenStore(db)
	ctx := context.Background()

	rec, err := st.GetToken(ctx, "legacy-token")
	if err != nil {
		t.Fatalf("GetToken failed: %v", err)
	}
	if rec == nil {
		t.Fatal("the migration must not lose the existing rows")
	}
	if rec.RevokedAt != nil {
		t.Error("an existing token must not come back revoked")
	}
	if rec.Uses != 1 || rec.UsedBy == nil || *rec.UsedBy != "node-old" {
		t.Errorf("the migration must keep uses and used_by: uses=%d used_by=%v", rec.Uses, rec.UsedBy)
	}

	revoked, err := st.RevokeToken(ctx, "legacy-token")
	if err != nil {
		t.Fatalf("the migrated row must be revocable: %v", err)
	}
	if revoked.RevokedAt == nil {
		t.Error("the migrated row must accept a revocation")
	}
}
