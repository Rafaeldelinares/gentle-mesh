package shell

import "testing"

// TestShell_ChainDB_BusyTimeout verifies that the shell opens its receipt chain
// database with the same PRAGMAs as the integration servers
// (integration/agent/server.go, integration/agent/server_shell.go): WAL journal
// mode and busy_timeout=5000. Without busy_timeout, concurrent writers sharing
// the same SQLite file can observe a raw SQLITE_BUSY instead of waiting.
func TestShell_ChainDB_BusyTimeout(t *testing.T) {
	chainDB := t.TempDir() + "/chain.db"

	s, err := New(Config{
		MeshID:       "mesh-test",
		AgentID:      "test-agent",
		WorkspaceDir: t.TempDir(),
		ChainDBPath:  chainDB,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	var busy int
	if err := s.db.QueryRow("PRAGMA busy_timeout;").Scan(&busy); err != nil {
		t.Fatalf("read busy_timeout: %v", err)
	}
	if busy != 5000 {
		t.Fatalf("chain DB busy_timeout = %d, want 5000", busy)
	}

	var mode string
	if err := s.db.QueryRow("PRAGMA journal_mode;").Scan(&mode); err != nil {
		t.Fatalf("read journal_mode: %v", err)
	}
	if mode != "wal" {
		t.Fatalf("chain DB journal_mode = %q, want %q", mode, "wal")
	}
}
