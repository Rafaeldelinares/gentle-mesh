package keystore

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

// Errors.
var (
	ErrAgentNotFound      = errors.New("keystore: agent not found")
	ErrAgentExists       = errors.New("keystore: agent already exists")
	ErrKeyNotGenerated   = errors.New("keystore: agent key not yet generated")
	ErrInvalidKeyFormat  = errors.New("keystore: invalid key format")
	ErrPermissionDenied  = errors.New("keystore: permission denied on key file")
)

// Store manages agent keys on disk.
type Store struct {
	mu         sync.RWMutex
	dir        string
	selfAgent  string
	registry   map[string]*AgentKey // agent_id -> key metadata
	privateKey ed25519.PrivateKey   // our own private key (cached)
}

// AgentKey is the metadata for a registered agent.
type AgentKey struct {
	AgentID      string `json:"agent_id"`
	PublicKeyHex string `json:"public_key_hex"`
	AddedAt      int64  `json:"added_at"`
}

// keystoreJSON is the on-disk registry format.
type keystoreJSON struct {
	Version  string      `json:"version"`
	SelfID  string      `json:"self_id"`
	Agents  []*AgentKey `json:"agents"`
}

// ─────────────────────────────────────────────────────────────────
// Constructor and lifecycle
// ─────────────────────────────────────────────────────────────────

// New creates or opens a keystore at the given directory.
// The directory is created if it does not exist.
// If generateIfMissing is true and no key exists for selfAgent,
// a new Ed25519 keypair is generated and stored.
func New(dir string, selfAgent string, generateIfMissing bool) (*Store, error) {
	if dir == "" {
		return nil, errors.New("keystore dir: empty")
	}
	if selfAgent == "" {
		return nil, errors.New("self agent id: empty")
	}

	// Ensure directory exists.
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, fmt.Errorf("create keystore dir: %w", err)
	}

	ks := &Store{
		dir:       dir,
		selfAgent: selfAgent,
		registry:  make(map[string]*AgentKey),
	}

	if err := ks.loadRegistry(); err != nil {
		return nil, fmt.Errorf("load registry: %w", err)
	}

	// Load our own private key.
	if err := ks.loadPrivateKey(); err != nil {
		if generateIfMissing && errors.Is(err, os.ErrNotExist) {
			if err := ks.generateAndSaveKey(); err != nil {
				return nil, fmt.Errorf("generate key: %w", err)
			}
		} else {
			return nil, fmt.Errorf("load private key: %w", err)
		}
	}

	return ks, nil
}

// ─────────────────────────────────────────────────────────────────
// Signing (Signer interface)
// ─────────────────────────────────────────────────────────────────

// Sign signs data with this agent's private key.
// Implements signing.Signer.
func (ks *Store) Sign(data []byte) (signature string, err error) {
	ks.mu.RLock()
	key := ks.privateKey
	ks.mu.RUnlock()

	if key == nil {
		return "", ErrKeyNotGenerated
	}
	if len(data) == 0 {
		return "", errors.New("sign: empty data")
	}

	// Ed25519 sign directly (the signer already has the hashed/prepared data).
	sig := ed25519.Sign(key, data)
	return signing.Base64Encode(sig), nil
}

// PublicKey returns this agent's public key bytes.
// Implements signing.Signer.
func (ks *Store) PublicKey() []byte {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	if ks.privateKey == nil {
		return nil
	}
	return ks.privateKey.Public().(ed25519.PublicKey)
}

// AgentID returns this agent's identifier.
func (ks *Store) AgentID() string {
	return ks.selfAgent
}

// ─────────────────────────────────────────────────────────────────
// Agent key management
// ─────────────────────────────────────────────────────────────────

// AddAgentKey stores another agent's public key in the registry.
// It does NOT overwrite existing keys unless overwrite is true.
func (ks *Store) AddAgentKey(agentID string, publicKeyHex string) error {
	ks.mu.Lock()
	defer ks.mu.Unlock()

	if agentID == "" {
		return errors.New("agent id: empty")
	}
	if agentID == ks.selfAgent {
		return errors.New("cannot add self as another agent")
	}

	if _, exists := ks.registry[agentID]; exists && !ks.isOwnedBySelf(agentID) {
		return fmt.Errorf("%w: %s", ErrAgentExists, agentID)
	}

	// Validate hex.
	pk, err := signing.HexToPublicKey(publicKeyHex)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidKeyFormat, err)
	}
	_ = pk // validated

	ks.registry[agentID] = &AgentKey{
		AgentID:      agentID,
		PublicKeyHex: publicKeyHex,
	}

	return ks.saveRegistry()
}

// isOwnedBySelf returns true if the agent is ourselves.
func (ks *Store) isOwnedBySelf(agentID string) bool {
	return agentID == ks.selfAgent
}

// GetAgentKey returns the public key metadata for an agent.
func (ks *Store) GetAgentKey(agentID string) (*AgentKey, error) {
	ks.mu.RLock()
	defer ks.mu.RUnlock()

	key, ok := ks.registry[agentID]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrAgentNotFound, agentID)
	}
	return key, nil
}

// GetAgentPublicKey returns the public key bytes for an agent.
func (ks *Store) GetAgentPublicKey(agentID string) ([]byte, error) {
	key, err := ks.GetAgentKey(agentID)
	if err != nil {
		return nil, err
	}
	return signing.HexToPublicKey(key.PublicKeyHex)
}

// ListAgents returns all registered agent IDs (excluding self).
func (ks *Store) ListAgents() []string {
	ks.mu.RLock()
	defer ks.mu.RUnlock()

	var agents []string
	for id := range ks.registry {
		if id != ks.selfAgent {
			agents = append(agents, id)
		}
	}
	sort.Strings(agents)
	return agents
}

// RemoveAgent deletes an agent from the registry.
func (ks *Store) RemoveAgent(agentID string) error {
	ks.mu.Lock()
	defer ks.mu.Unlock()

	if agentID == ks.selfAgent {
		return errors.New("cannot remove self")
	}
	if _, ok := ks.registry[agentID]; !ok {
		return fmt.Errorf("%w: %s", ErrAgentNotFound, agentID)
	}

	delete(ks.registry, agentID)
	return ks.saveRegistry()
}

// SelfAgent returns this keystore's agent ID.
func (ks *Store) SelfAgent() string {
	return ks.selfAgent
}

// ─────────────────────────────────────────────────────────────────
// Disk I/O
// ─────────────────────────────────────────────────────────────────

func (ks *Store) loadRegistry() error {
	path := filepath.Join(ks.dir, "keystore.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // empty registry
		}
		return err
	}

	var reg keystoreJSON
	if err := json.Unmarshal(data, &reg); err != nil {
		return fmt.Errorf("parse registry: %w", err)
	}

	for _, ak := range reg.Agents {
		ks.registry[ak.AgentID] = ak
	}
	return nil
}

func (ks *Store) saveRegistry() error {
	path := filepath.Join(ks.dir, "keystore.json")

	var agents []*AgentKey
	for _, ak := range ks.registry {
		agents = append(agents, ak)
	}

	reg := keystoreJSON{
		Version: "1.0",
		SelfID:  ks.selfAgent,
		Agents:  agents,
	}

	data, err := json.Marshal(reg)
	if err != nil {
		return fmt.Errorf("marshal registry: %w", err)
	}

	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write registry: %w", err)
	}

	return nil
}

func (ks *Store) loadPrivateKey() error {
	path := ks.privateKeyPath()
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	key, err := signing.ParsePrivateKeyPEM(data)
	if err != nil {
		return fmt.Errorf("parse private key: %w", err)
	}

	ks.privateKey = key
	return nil
}

func (ks *Store) savePrivateKey() error {
	path := ks.privateKeyPath()
	data, err := signing.MarshalPrivateKeyPEM(ks.privateKey)
	if err != nil {
		return fmt.Errorf("marshal private key: %w", err)
	}

	// Write with restrictive permissions.
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write private key: %w", err)
	}

	return nil
}

func (ks *Store) privateKeyPath() string {
	return filepath.Join(ks.dir, "private.pem")
}

func (ks *Store) generateAndSaveKey() error {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return fmt.Errorf("generate ed25519 key: %w", err)
	}

	ks.privateKey = priv

	if err := ks.savePrivateKey(); err != nil {
		return err
	}

	// Register ourselves in the registry.
	pubKeyHex := signing.PublicKeyToHex(priv.Public().(ed25519.PublicKey))
	ks.registry[ks.selfAgent] = &AgentKey{
		AgentID:      ks.selfAgent,
		PublicKeyHex: pubKeyHex,
	}

	return ks.saveRegistry()
}

// ExportAgentCard returns this agent's public information for sharing.
// Use this to share your public key with other agents.
func (ks *Store) ExportAgentCard() *AgentCard {
	return &AgentCard{
		AgentID:   ks.selfAgent,
		PublicKey: signing.PublicKeyToHex(ks.PublicKey()),
	}
}

// AgentCard is the shareable public identity of an agent.
// This is what agent B publishes so agent A can add B to its keystore.
type AgentCard struct {
	AgentID   string `json:"agent_id"`
	PublicKey string `json:"public_key"`
}

// ImportAgentCard adds an agent to the keystore from an AgentCard.
func (ks *Store) ImportAgentCard(card *AgentCard) error {
	return ks.AddAgentKey(card.AgentID, card.PublicKey)
}
