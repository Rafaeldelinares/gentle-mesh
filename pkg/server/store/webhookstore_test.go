package store

import (
	"context"
	"database/sql"
	"testing"
	"time"
)

func setupTestWebhookDB(t *testing.T) *sql.DB {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	if err := InitWebhookSchema(db); err != nil {
		db.Close()
		t.Fatalf("failed to init webhook schema: %v", err)
	}
	return db
}

func TestWebhookStoreCreateAndGet(t *testing.T) {
	db := setupTestWebhookDB(t)
	defer db.Close()

	store := NewSQLiteWebhookStore(db)
	ctx := context.Background()

	webhook := &WebhookRecord{
		ID:        "wh-123",
		URL:       "https://example.com/webhook",
		Secret:    "my-secret",
		Events:    []string{"task.completed", "task.failed"},
		CreatedAt: time.Now(),
		Active:    true,
	}

	err := store.CreateWebhook(ctx, webhook)
	if err != nil {
		t.Fatalf("CreateWebhook failed: %v", err)
	}

	retrieved, err := store.GetWebhook(ctx, "wh-123")
	if err != nil {
		t.Fatalf("GetWebhook failed: %v", err)
	}

	if retrieved == nil {
		t.Fatal("GetWebhook returned nil")
	}
	if retrieved.URL != webhook.URL {
		t.Errorf("expected URL %s, got %s", webhook.URL, retrieved.URL)
	}
}

func TestWebhookStoreDelete(t *testing.T) {
	db := setupTestWebhookDB(t)
	defer db.Close()

	store := NewSQLiteWebhookStore(db)
	ctx := context.Background()

	webhook := &WebhookRecord{
		ID:        "wh-delete",
		URL:       "https://example.com/webhook",
		Events:    []string{"task.completed"},
		CreatedAt: time.Now(),
		Active:    true,
	}

	store.CreateWebhook(ctx, webhook)
	err := store.DeleteWebhook(ctx, "wh-delete")
	if err != nil {
		t.Fatalf("DeleteWebhook failed: %v", err)
	}

	retrieved, _ := store.GetWebhook(ctx, "wh-delete")
	if retrieved != nil {
		t.Error("Webhook should be deleted")
	}
}

func TestWebhookStoreListActive(t *testing.T) {
	db := setupTestWebhookDB(t)
	defer db.Close()

	store := NewSQLiteWebhookStore(db)
	ctx := context.Background()

	// Create webhooks with different events
	webhooks := []*WebhookRecord{
		{ID: "wh-1", URL: "https://a.com", Events: []string{"task.completed"}, CreatedAt: time.Now(), Active: true},
		{ID: "wh-2", URL: "https://b.com", Events: []string{"task.failed"}, CreatedAt: time.Now(), Active: true},
		{ID: "wh-3", URL: "https://c.com", Events: []string{"task.completed"}, CreatedAt: time.Now(), Active: false},
	}

	for _, w := range webhooks {
		store.CreateWebhook(ctx, w)
	}

	// List active for task.completed
	active, err := store.ListActiveWebhooks(ctx, "task.completed")
	if err != nil {
		t.Fatalf("ListActiveWebhooks failed: %v", err)
	}
	if len(active) != 1 {
		t.Errorf("expected 1 active webhook, got %d", len(active))
	}
}
