package store

import (
	"context"
	"database/sql"
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
		MaxUses:  1,
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
		MaxUses:  1,
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
		MaxUses:  2,
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
