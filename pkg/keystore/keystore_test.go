package keystore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

func TestNew_GeneratesKey(t *testing.T) {
	dir := t.TempDir()

	ks, err := New(dir, "agent-b", true)
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}

	if ks.AgentID() != "agent-b" {
		t.Errorf("AgentID() = %q, want %q", ks.AgentID(), "agent-b")
	}

	pk := ks.PublicKey()
	if len(pk) != 32 {
		t.Errorf("PublicKey() length = %d, want 32", len(pk))
	}

	sig, err := ks.Sign([]byte("test data"))
	if err != nil {
		t.Fatalf("Sign() error = %v, want nil", err)
	}
	if sig == "" {
		t.Error("Sign() returned empty signature")
	}

	key, err := ks.GetAgentKey("agent-b")
	if err != nil {
		t.Errorf("GetAgentKey(self) error = %v, want nil", err)
	}
	if key.AgentID != "agent-b" {
		t.Errorf("Registry self key agent_id = %q, want %q", key.AgentID, "agent-b")
	}
}

func TestNew_ReusesExistingKey(t *testing.T) {
	dir := t.TempDir()

	ks1, err := New(dir, "agent-c", true)
	if err != nil {
		t.Fatalf("New() first error = %v", err)
	}
	sig1, _ := ks1.Sign([]byte("persistence test"))

	ks2, err := New(dir, "agent-c", false)
	if err != nil {
		t.Fatalf("New() reopen error = %v", err)
	}

	sig2, _ := ks2.Sign([]byte("persistence test"))
	if sig1 != sig2 {
		t.Errorf("Reopened signer produces different signature: %q != %q", sig1, sig2)
	}
}

func TestNew_EmptyAgentID(t *testing.T) {
	_, err := New(t.TempDir(), "", true)
	if err == nil {
		t.Error("New(empty agent_id) expected error, got nil")
	}
}

func TestNew_EmptyDirPath(t *testing.T) {
	_, err := New("", "agent-e", true)
	if err == nil {
		t.Error("New(empty dir) expected error, got nil")
	}
}

func TestNew_CreatesFiles(t *testing.T) {
	dir := t.TempDir()
	_, err := New(dir, "agent-d", true)
	if err != nil {
		t.Fatalf("New() error = %v, want nil", err)
	}

	for _, f := range []string{"private.pem", "keystore.json"} {
		path := filepath.Join(dir, f)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			t.Errorf("expected file %q not found", f)
		}
	}
}

// ─────────────────────────────────────────────────────────────────
// Signing
// ─────────────────────────────────────────────────────────────────

func TestSign_AndVerify(t *testing.T) {
	ks, _ := New(t.TempDir(), "agent-signer", true)
	data := []byte("sign me")

	sig, err := ks.Sign(data)
	if err != nil {
		t.Fatalf("Sign() error = %v, want nil", err)
	}

	err = signing.Verify(ks.PublicKey(), data, sig)
	if err != nil {
		t.Errorf("Verify() error = %v, want nil", err)
	}
}

func TestSign_EmptyData(t *testing.T) {
	ks, _ := New(t.TempDir(), "agent-f", true)
	_, err := ks.Sign([]byte{})
	if err == nil {
		t.Error("Sign(empty) expected error, got nil")
	}
}

func TestSign_Deterministic(t *testing.T) {
	ks, _ := New(t.TempDir(), "agent-det", true)
	data := []byte("deterministic")

	sig1, _ := ks.Sign(data)
	sig2, _ := ks.Sign(data)

	if sig1 != sig2 {
		t.Errorf("Sign() not deterministic: %q != %q", sig1, sig2)
	}
}

// ─────────────────────────────────────────────────────────────────
// Agent management
// ─────────────────────────────────────────────────────────────────

func TestAddAgentKey(t *testing.T) {
	ks, _ := New(t.TempDir(), "agent-a", true)

	other, _ := signing.GenerateSigner("agent-b")
	otherHex := other.PublicKeyHex()

	err := ks.AddAgentKey("agent-b", otherHex)
	if err != nil {
		t.Fatalf("AddAgentKey() error = %v, want nil", err)
	}

	key, err := ks.GetAgentKey("agent-b")
	if err != nil {
		t.Fatalf("GetAgentKey() error = %v, want nil", err)
	}
	if key.AgentID != "agent-b" {
		t.Errorf("GetAgentKey().AgentID = %q, want %q", key.AgentID, "agent-b")
	}
	if key.PublicKeyHex != otherHex {
		t.Errorf("GetAgentKey().PublicKeyHex = %q, want %q", key.PublicKeyHex, otherHex)
	}
}

func TestAddAgentKey_Duplicate(t *testing.T) {
	ks, _ := New(t.TempDir(), "agent-a", true)

	other, _ := signing.GenerateSigner("agent-c")
	ks.AddAgentKey("agent-c", other.PublicKeyHex())

	err := ks.AddAgentKey("agent-c", other.PublicKeyHex())
	if err == nil {
		t.Error("AddAgentKey(duplicate) expected error, got nil")
	}
}

func TestAddAgentKey_InvalidHex(t *testing.T) {
	ks, _ := New(t.TempDir(), "agent-a", true)

	err := ks.AddAgentKey("agent-x", "not-a-hex-key")
	if err == nil {
		t.Error("AddAgentKey(invalid hex) expected error, got nil")
	}
}

func TestAddAgentKey_EmptyAgentID(t *testing.T) {
	ks, _ := New(t.TempDir(), "agent-a", true)

	err := ks.AddAgentKey("", "abc123")
	if err == nil {
		t.Error("AddAgentKey(empty agent_id) expected error, got nil")
	}
}

func TestAddAgentKey_Self(t *testing.T) {
	ks, _ := New(t.TempDir(), "agent-a", true)

	err := ks.AddAgentKey("agent-a", signing.PublicKeyToHex(ks.PublicKey()))
	if err == nil {
		t.Error("AddAgentKey(self) expected error, got nil")
	}
}

func TestGetAgentKey_NotFound(t *testing.T) {
	ks, _ := New(t.TempDir(), "agent-a", true)

	_, err := ks.GetAgentKey("nonexistent")
	if err == nil {
		t.Error("GetAgentKey(nonexistent) expected error, got nil")
	}
}

func TestGetAgentPublicKey(t *testing.T) {
	ks, _ := New(t.TempDir(), "agent-a", true)

	other, _ := signing.GenerateSigner("agent-b")
	ks.AddAgentKey("agent-b", other.PublicKeyHex())

	pk, err := ks.GetAgentPublicKey("agent-b")
	if err != nil {
		t.Fatalf("GetAgentPublicKey() error = %v, want nil", err)
	}

	if string(pk) != string(other.PublicKey()) {
		t.Error("GetAgentPublicKey() mismatch")
	}
}

func TestListAgents(t *testing.T) {
	ks, _ := New(t.TempDir(), "agent-main", true)

	for i := 0; i < 5; i++ {
		other, _ := signing.GenerateSigner("agent-" + string(rune('a'+i)))
		ks.AddAgentKey("agent-"+string(rune('a'+i)), other.PublicKeyHex())
	}

	agents := ks.ListAgents()
	if len(agents) != 5 {
		t.Errorf("ListAgents() count = %d, want 5", len(agents))
	}

	for i := 1; i < len(agents); i++ {
		if agents[i] <= agents[i-1] {
			t.Errorf("ListAgents() not sorted: %q >= %q", agents[i-1], agents[i])
		}
	}

	for _, id := range agents {
		if id == "agent-main" {
			t.Error("ListAgents() includes self")
		}
	}
}

func TestRemoveAgent(t *testing.T) {
	ks, _ := New(t.TempDir(), "agent-a", true)

	other, _ := signing.GenerateSigner("agent-remove")
	ks.AddAgentKey("agent-remove", other.PublicKeyHex())

	_, err := ks.GetAgentKey("agent-remove")
	if err != nil {
		t.Fatalf("GetAgentKey before remove error = %v", err)
	}

	err = ks.RemoveAgent("agent-remove")
	if err != nil {
		t.Fatalf("RemoveAgent() error = %v, want nil", err)
	}

	_, err = ks.GetAgentKey("agent-remove")
	if err == nil {
		t.Error("GetAgentKey after remove: expected error, got nil")
	}
}

func TestRemoveAgent_Self(t *testing.T) {
	ks, _ := New(t.TempDir(), "agent-a", true)
	err := ks.RemoveAgent("agent-a")
	if err == nil {
		t.Error("RemoveAgent(self) expected error, got nil")
	}
}

func TestRemoveAgent_NotFound(t *testing.T) {
	ks, _ := New(t.TempDir(), "agent-a", true)
	err := ks.RemoveAgent("nonexistent")
	if err == nil {
		t.Error("RemoveAgent(nonexistent) expected error, got nil")
	}
}

// ─────────────────────────────────────────────────────────────────
// Agent Card
// ─────────────────────────────────────────────────────────────────

func TestExportAgentCard(t *testing.T) {
	ks, _ := New(t.TempDir(), "agent-card", true)

	card := ks.ExportAgentCard()
	if card.AgentID != "agent-card" {
		t.Errorf("AgentCard.AgentID = %q, want %q", card.AgentID, "agent-card")
	}
	if card.PublicKey == "" {
		t.Error("AgentCard.PublicKey is empty")
	}
	if card.PublicKey != signing.PublicKeyToHex(ks.PublicKey()) {
		t.Errorf("AgentCard.PublicKey mismatch")
	}
}

func TestImportAgentCard(t *testing.T) {
	ks1, _ := New(t.TempDir(), "agent-source", true)
	ks2, _ := New(t.TempDir(), "agent-dest", true)

	card := ks1.ExportAgentCard()

	err := ks2.ImportAgentCard(card)
	if err != nil {
		t.Fatalf("ImportAgentCard() error = %v, want nil", err)
	}

	pk, err := ks2.GetAgentPublicKey("agent-source")
	if err != nil {
		t.Fatalf("GetAgentPublicKey() error = %v, want nil", err)
	}

	if string(pk) != string(ks1.PublicKey()) {
		t.Error("Imported key mismatch")
	}
}

// ─────────────────────────────────────────────────────────────────
// Persistence
// ─────────────────────────────────────────────────────────────────

func TestPersistence_RegistryPersists(t *testing.T) {
	dir := t.TempDir()

	ks1, _ := New(dir, "agent-persist", true)
	for i := 0; i < 3; i++ {
		other, _ := signing.GenerateSigner("other-" + string(rune('0'+i)))
		ks1.AddAgentKey("other-"+string(rune('0'+i)), other.PublicKeyHex())
	}

	ks2, _ := New(dir, "agent-persist", false)

	agents := ks2.ListAgents()
	if len(agents) != 3 {
		t.Errorf("ListAgents() after reopen = %d, want 3", len(agents))
	}

	for _, id := range agents {
		_, err := ks2.GetAgentPublicKey(id)
		if err != nil {
			t.Errorf("GetAgentPublicKey(%s) error = %v", id, err)
		}
	}
}

func TestPersistence_PrivateKeyPermissions(t *testing.T) {
	dir := t.TempDir()
	ks, _ := New(dir, "agent-perm", true)

	path := filepath.Join(dir, "private.pem")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat(private.pem) error = %v", err)
	}

	perm := info.Mode().Perm()
	if perm != 0600 {
		t.Errorf("private.pem permissions = %o, want 0600", perm)
	}

	// Verify the key is usable.
	sig, err := ks.Sign([]byte("test"))
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	if sig == "" {
		t.Error("Sign() returned empty")
	}
}

func TestSelfAgent(t *testing.T) {
	ks, _ := New(t.TempDir(), "my-agent", true)
	if ks.SelfAgent() != "my-agent" {
		t.Errorf("SelfAgent() = %q, want %q", ks.SelfAgent(), "my-agent")
	}
}
