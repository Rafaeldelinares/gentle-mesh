package a2a

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gentleman-programming/gentle-mesh/pkg/jcs"
	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

const (
	// KindAgentCardV1 is the typed discriminator for gentle-mesh Agent Cards.
	KindAgentCardV1 = "gentle-mesh.agent-card/v1"
)

var (
	// ErrInvalidSignature indicates the Ed25519 signature over JCS(protected) + "." + JCS(payload) failed.
	ErrInvalidSignature = errors.New("invalid agent card signature")

	// ErrMalformedEnvelope indicates missing or invalid fields in the JWS envelope.
	ErrMalformedEnvelope = errors.New("malformed agent card envelope")

	// ErrSubjectKeyMismatch indicates the signing key does not match the subject's declared public key.
	ErrSubjectKeyMismatch = errors.New("signing key does not match card subject public key")
)

// AgentCardSubject identifies the principal described by the card.
type AgentCardSubject struct {
	NodeID    string `json:"node_id"`
	Role      string `json:"role"`                 // "coordinator", "worker"
	PublicKey string `json:"public_key,omitempty"` // Hex or base64url Ed25519 public key
}

// AgentCardCapabilities describes the operational capabilities of the agent.
type AgentCardCapabilities struct {
	Streaming               bool `json:"streaming"`
	PushNotifications       bool `json:"push_notifications"`
	StateTransitionHistory bool `json:"state_transition_history"`
}

// AgentCardSkill describes a distinct capability or skill offered by the agent.
type AgentCardSkill struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Tags        []string `json:"tags,omitempty"`
}

// AgentCardMesh links the card to an exact verifiable point in the coordinator's join log.
type AgentCardMesh struct {
	LogHeadSeq  int64  `json:"log_head_seq"`
	LogHeadHash string `json:"log_head_hash"`
	StateDigest string `json:"state_digest"`
}

// AgentCard represents the canonical body of an Agent Card (RFC-003 §4.2.1.1).
type AgentCard struct {
	Kind         string                `json:"kind"`
	Subject      AgentCardSubject      `json:"subject"`
	Name         string                `json:"name"`
	Description  string                `json:"description,omitempty"`
	URL          string                `json:"url"`
	Version      string                `json:"version,omitempty"`
	Capabilities AgentCardCapabilities `json:"capabilities"`
	Skills       []AgentCardSkill      `json:"skills,omitempty"`
	Mesh         AgentCardMesh         `json:"mesh"`
	IssuedAt     string                `json:"issued_at"`
	ExpiresAt    string                `json:"expires_at"`
}

// JWSProtectedHeader is the JOSE protected header for the compact JWS envelope.
type JWSProtectedHeader struct {
	Alg string `json:"alg"` // Always "EdDSA"
	Typ string `json:"typ"` // Always "JOSE"
	Kid string `json:"kid"` // Subject's node_id
}

// AgentCardEnvelope is the JWS envelope wrapping the canonical AgentCard.
type AgentCardEnvelope struct {
	Protected JWSProtectedHeader `json:"protected"`
	Payload   string             `json:"payload"`   // base64url(JCS(card))
	Signature string             `json:"signature"` // base64url(Ed25519 signature)
}

// EncodeCanonicalPayload returns the base64url-encoded JCS representation of an AgentCard.
func EncodeCanonicalPayload(card *AgentCard) (string, error) {
	if card == nil {
		return "", errors.New("cannot encode nil card")
	}
	canonical, err := jcs.Marshal(card)
	if err != nil {
		return "", fmt.Errorf("canonicalize card: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(canonical), nil
}

// SignAgentCard signs card using signer and returns a compact JWS AgentCardEnvelope.
// Enforces that the signer's AgentID matches card.Subject.NodeID.
func SignAgentCard(card *AgentCard, signer signing.Signer) (*AgentCardEnvelope, error) {
	if card == nil || signer == nil {
		return nil, errors.New("card and signer must not be nil")
	}

	// Ensure Subject.PublicKey is populated from signer if empty
	if card.Subject.PublicKey == "" {
		card.Subject.PublicKey = hex.EncodeToString(signer.PublicKey())
	}

	payloadB64, err := EncodeCanonicalPayload(card)
	if err != nil {
		return nil, err
	}

	protected := JWSProtectedHeader{
		Alg: "EdDSA",
		Typ: "JOSE",
		Kid: card.Subject.NodeID,
	}
	protCanonical, err := jcs.Marshal(protected)
	if err != nil {
		return nil, fmt.Errorf("canonicalize protected header: %w", err)
	}
	protB64 := base64.RawURLEncoding.EncodeToString(protCanonical)

	// Signing input is: base64url(JCS(protected)) || '.' || base64url(JCS(payload))
	signingInput := protB64 + "." + payloadB64
	// signer.Sign returns a base64url-encoded signature string
	sigB64, err := signer.Sign([]byte(signingInput))
	if err != nil {
		return nil, fmt.Errorf("sign agent card: %w", err)
	}

	return &AgentCardEnvelope{
		Protected: protected,
		Payload:   payloadB64,
		Signature: sigB64,
	}, nil
}

// VerifyAgentCard cryptographically verifies the envelope's Ed25519 signature against
// the subject's public key declared in the card, and returns the decoded AgentCard.
func VerifyAgentCard(env *AgentCardEnvelope) (*AgentCard, error) {
	if env == nil || env.Payload == "" || env.Signature == "" {
		return nil, ErrMalformedEnvelope
	}
	if env.Protected.Alg != "EdDSA" {
		return nil, fmt.Errorf("unsupported alg %q: expected EdDSA", env.Protected.Alg)
	}

	// 1. Decode payload
	payloadBytes, err := base64.RawURLEncoding.DecodeString(env.Payload)
	if err != nil {
		// Fallback to standard url encoding with padding if needed
		payloadBytes, err = base64.URLEncoding.DecodeString(env.Payload)
		if err != nil {
			return nil, fmt.Errorf("decode payload: %w", err)
		}
	}

	var card AgentCard
	if err := json.Unmarshal(payloadBytes, &card); err != nil {
		return nil, fmt.Errorf("unmarshal payload: %w", err)
	}

	// 2. Extract and validate subject's public key
	pubHex := strings.TrimSpace(card.Subject.PublicKey)
	if pubHex == "" {
		return nil, errors.New("card subject has no public key")
	}

	pubBytes, err := signing.ValidateEd25519PublicKey(pubHex)
	if err != nil {
		// Try base64url decode if not hex
		var b64Err error
		pubBytes, b64Err = base64.RawURLEncoding.DecodeString(pubHex)
		if b64Err != nil || len(pubBytes) != 32 {
			return nil, fmt.Errorf("invalid subject public key: %w", err)
		}
	}

	// 3. Reconstruct signing input: base64url(JCS(protected)) || '.' || env.Payload
	protCanonical, err := jcs.Marshal(env.Protected)
	if err != nil {
		return nil, err
	}
	protB64 := base64.RawURLEncoding.EncodeToString(protCanonical)
	signingInput := protB64 + "." + env.Payload

	// 4. Decode signature
	sigBytes, err := base64.RawURLEncoding.DecodeString(env.Signature)
	if err != nil {
		sigBytes, err = base64.URLEncoding.DecodeString(env.Signature)
		if err != nil {
			return nil, fmt.Errorf("decode signature: %w", err)
		}
	}

	if len(sigBytes) != ed25519.SignatureSize {
		return nil, fmt.Errorf("invalid signature size: %d", len(sigBytes))
	}

	// 5. Verify Ed25519 signature
	if !ed25519.Verify(pubBytes, []byte(signingInput), sigBytes) {
		return nil, ErrInvalidSignature
	}

	return &card, nil
}
