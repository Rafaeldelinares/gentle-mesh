package a2a_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/a2a"
	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

func TestAgentCard_SignAndVerify_Success(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate ed25519 key: %v", err)
	}

	signer, err := signing.NewBasicSigner(priv, "node-worker-1")
	if err != nil {
		t.Fatalf("failed to create signer: %v", err)
	}

	card := &a2a.AgentCard{
		Kind: a2a.KindAgentCardV1,
		Subject: a2a.AgentCardSubject{
			NodeID:    "node-worker-1",
			Role:      "worker",
			PublicKey: hex.EncodeToString(pub),
		},
		Name:        "node-worker-1",
		Description: "Worker node specializing in Go and Python tasks",
		URL:         "https://10.0.0.42:9099",
		Version:     "1.0.0",
		Capabilities: a2a.AgentCardCapabilities{
			Streaming:               true,
			PushNotifications:       false,
			StateTransitionHistory: true,
		},
		Skills: []a2a.AgentCardSkill{
			{
				ID:          "go-build",
				Name:        "Go Compiler",
				Description: "Builds and tests Go projects",
				Tags:        []string{"go", "compiler"},
			},
		},
		Mesh: a2a.AgentCardMesh{
			LogHeadSeq:  42,
			LogHeadHash: "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
			StateDigest: "sha256:fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210",
		},
		IssuedAt:  time.Now().UTC().Format(time.RFC3339),
		ExpiresAt: time.Now().UTC().Add(30 * 24 * time.Hour).Format(time.RFC3339),
	}

	// 1. Sign
	env, err := a2a.SignAgentCard(card, signer)
	if err != nil {
		t.Fatalf("failed to sign agent card: %v", err)
	}

	if env.Protected.Alg != "EdDSA" || env.Protected.Typ != "JOSE" {
		t.Errorf("unexpected protected header: %+v", env.Protected)
	}
	if env.Protected.Kid != "node-worker-1" {
		t.Errorf("expected kid 'node-worker-1', got %q", env.Protected.Kid)
	}

	// 2. Verify
	verifiedCard, err := a2a.VerifyAgentCard(env)
	if err != nil {
		t.Fatalf("expected valid card verification, got: %v", err)
	}

	if verifiedCard.Subject.NodeID != "node-worker-1" {
		t.Errorf("expected node_id 'node-worker-1', got %q", verifiedCard.Subject.NodeID)
	}
	if verifiedCard.Mesh.LogHeadSeq != 42 {
		t.Errorf("expected LogHeadSeq 42, got %d", verifiedCard.Mesh.LogHeadSeq)
	}
}

func TestAgentCard_TamperedPayload_Rejected(t *testing.T) {
	_, priv, _ := ed25519.GenerateKey(rand.Reader)
	signer, _ := signing.NewBasicSigner(priv, "node-worker-1")

	card := &a2a.AgentCard{
		Kind: a2a.KindAgentCardV1,
		Subject: a2a.AgentCardSubject{
			NodeID: "node-worker-1",
			Role:   "worker",
		},
		Name: "node-worker-1",
		Capabilities: a2a.AgentCardCapabilities{
			Streaming: false,
		},
		IssuedAt:  time.Now().UTC().Format(time.RFC3339),
		ExpiresAt: time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339),
	}

	env, err := a2a.SignAgentCard(card, signer)
	if err != nil {
		t.Fatalf("sign failed: %v", err)
	}

	// Tamper payload by modifying decoded card and re-encoding payload without re-signing
	tamperedCard := *card
	tamperedCard.Capabilities.Streaming = true // Attacker claims streaming capability!

	tamperedPayloadB64, err := a2a.EncodeCanonicalPayload(&tamperedCard)
	if err != nil {
		t.Fatalf("failed to encode canonical payload: %v", err)
	}

	tamperedEnv := *env
	tamperedEnv.Payload = tamperedPayloadB64 // Replace payload with tampered one

	_, err = a2a.VerifyAgentCard(&tamperedEnv)
	if err == nil {
		t.Fatalf("expected verification failure on tampered card payload, got nil")
	}
}

func TestAgentCard_CoordinatorDoesNotSignForThirdParties(t *testing.T) {
	// Worker's key
	workerPub, _, _ := ed25519.GenerateKey(rand.Reader)
	workerPubHex := hex.EncodeToString(workerPub)

	// Coordinator tries to sign a card claiming the worker as subject using Coordinator's private key
	_, coordPriv, _ := ed25519.GenerateKey(rand.Reader)
	coordSigner, _ := signing.NewBasicSigner(coordPriv, "coordinator")

	workerCard := &a2a.AgentCard{
		Kind: a2a.KindAgentCardV1,
		Subject: a2a.AgentCardSubject{
			NodeID:    "worker-node",
			Role:      "worker",
			PublicKey: workerPubHex, // Subject's public key is Worker's
		},
		Name:      "worker-node",
		IssuedAt:  time.Now().UTC().Format(time.RFC3339),
		ExpiresAt: time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339),
	}

	// Coordinator signs worker's card
	env, err := a2a.SignAgentCard(workerCard, coordSigner)
	if err != nil {
		t.Fatalf("signing failed: %v", err)
	}

	// When an external consumer verifies the card against the subject's published key,
	// it MUST fail because the signature was produced by the coordinator, not the subject!
	_, err = a2a.VerifyAgentCard(env)
	if err == nil {
		t.Fatalf("expected verification failure when coordinator signs on behalf of worker, got nil")
	}
}
