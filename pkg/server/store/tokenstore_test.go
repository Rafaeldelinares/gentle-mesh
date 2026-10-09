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

	used, err := store.UseToken(ctx, "use-token-456")
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
	_, err = store.UseToken(ctx, "multi-use-token")
	if err != nil {
		t.Fatalf("First UseToken failed: %v", err)
	}

	_, err = store.UseToken(ctx, "multi-use-token")
	if err != nil {
		t.Fatalf("Second UseToken failed: %v", err)
	}

	// Third use should fail
	_, err = store.UseToken(ctx, "multi-use-token")
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

	_, err = store.UseToken(ctx, "expired-token")
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
			_, errs[i] = store.UseToken(ctx, "race-token")
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
		_, err := store.UseToken(ctx, "zero-use-token")
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
			_, errs[i] = store.UseToken(ctx, "expired-race-token")
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
		if _, err := store.UseToken(ctx, "no-expiry-token"); err != nil {
			t.Fatalf("attempt %d: a token without expiry must be usable: %v", attempt, err)
		}
	}

	_, err := store.UseToken(ctx, "no-expiry-token")
	if err == nil || err.Error() != "token max uses exceeded" {
		t.Fatalf("third use: expected 'token max uses exceeded', got %v", err)
	}
}
