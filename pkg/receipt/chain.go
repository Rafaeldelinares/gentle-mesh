package receipt

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/jcs"
	"github.com/gentleman-programming/gentle-mesh/pkg/signing"
)

// Errors for chain operations.
var (
	ErrReceiptNotFound         = errors.New("receipt not found")
	ErrChainBroken             = errors.New("receipt chain is broken")
	ErrChainVerificationFailed = errors.New("chain verification failed")
	ErrInvalidReceipt          = errors.New("invalid receipt")
	ErrInvalidPreviousHash     = fmt.Errorf("%w: invalid previous receipt hash", ErrChainBroken)
	ErrSequenceConflict        = errors.New("sequence conflict")
)

// ChainStore manages the receipt chain for an agent pair.
// It handles persistence and cryptographic chain verification.
//
// Concurrent safety: SaveReceipt is serialized with a mutex so that
// concurrent goroutines never compute the same prev_hash. The mutex
// is per ChainStore instance; multiple instances can be used in parallel.
type ChainStore struct {
	db *sql.DB
	mu sync.Mutex
}

// NewChainStore creates a ChainStore backed by the given SQLite database.
func NewChainStore(db *sql.DB) *ChainStore {
	return &ChainStore{db: db}
}

// InitSchema creates the receipts table and indexes.
func (cs *ChainStore) InitSchema(ctx context.Context) error {
	schema := `
	CREATE TABLE IF NOT EXISTS receipts (
		receipt_id            TEXT NOT NULL,
		contract_id           TEXT NOT NULL,
		envelope_hash         TEXT NOT NULL,
		emitter_agent_id      TEXT NOT NULL,
		executor_agent_id     TEXT NOT NULL,
		verdict               TEXT NOT NULL,
		previous_receipt_hash TEXT,
		executor_signature    TEXT NOT NULL,
		executor_signed_at    TEXT NOT NULL,
		emitter_acceptance    TEXT,
		emitter_acceptance_at TEXT,
		emitter_signature     TEXT,
		dispute_reason        TEXT,
		data                  TEXT NOT NULL,
		seq                   INTEGER NOT NULL DEFAULT 0
	);

	CREATE INDEX IF NOT EXISTS idx_receipts_pair
		ON receipts(emitter_agent_id, executor_agent_id, executor_signed_at);

	CREATE INDEX IF NOT EXISTS idx_receipts_contract
		ON receipts(contract_id);

	CREATE UNIQUE INDEX IF NOT EXISTS idx_receipts_pair_seq
		ON receipts(emitter_agent_id, executor_agent_id, seq);
	`
	_, err := cs.db.ExecContext(ctx, schema)
	if err != nil {
		return err
	}
	// Migrate existing tables that might not have the seq column.
	_, _ = cs.db.ExecContext(ctx, `ALTER TABLE receipts ADD COLUMN seq INTEGER NOT NULL DEFAULT 0;`)
	_, err = cs.db.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_receipts_pair_seq ON receipts(emitter_agent_id, executor_agent_id, seq);`)
	return err
}

// SaveReceipt persists a receipt to the chain atomically using a SQLite transaction.
// It enforces S7 (chain integrity on write) and R4 (strictly monotonic sequence).
func (cs *ChainStore) SaveReceipt(ctx context.Context, r *SettlementReceipt) error {
	if r == nil {
		return ErrInvalidReceipt
	}
	if err := ValidateReceipt(r); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidReceipt, err)
	}

	cs.mu.Lock()
	defer cs.mu.Unlock()

	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	// Query last receipt for this pair inside the transaction.
	var lastData string
	var lastSeq int64
	err = tx.QueryRowContext(ctx,
		`SELECT data, seq FROM receipts
		 WHERE emitter_agent_id = ? AND executor_agent_id = ?
		 ORDER BY seq DESC, executor_signed_at DESC LIMIT 1`,
		r.EmitterAgentID, r.ExecutorAgentID).Scan(&lastData, &lastSeq)

	var expectedSeq int64
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			// First receipt for this pair (S7): previous_receipt_hash must be empty.
			if r.PreviousReceiptHash != "" {
				return fmt.Errorf("%w: first receipt must have empty previous_receipt_hash", ErrInvalidPreviousHash)
			}
			expectedSeq = 1
		} else {
			return fmt.Errorf("query last receipt: %w", err)
		}
	} else {
		// Subsequent receipt for this pair (S7): previous_receipt_hash must match previous executor signature hash.
		var last SettlementReceipt
		if unmarshalErr := json.Unmarshal([]byte(lastData), &last); unmarshalErr != nil {
			return fmt.Errorf("unmarshal last receipt: %w", unmarshalErr)
		}

		h := sha256.Sum256([]byte(last.ExecutorSignature))
		expectedPrevHash := hex.EncodeToString(h[:])
		if r.PreviousReceiptHash != expectedPrevHash {
			return fmt.Errorf("%w: expected %s, got %s", ErrInvalidPreviousHash, expectedPrevHash, r.PreviousReceiptHash)
		}
		expectedSeq = lastSeq + 1
	}

	// Validate or assign sequence number (R4).
	if r.SequenceNumber != 0 && r.SequenceNumber != expectedSeq {
		return fmt.Errorf("%w: expected sequence %d, got %d", ErrSequenceConflict, expectedSeq, r.SequenceNumber)
	}
	r.SequenceNumber = expectedSeq

	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal receipt: %w", err)
	}

	query := `
	INSERT INTO receipts (
		receipt_id, contract_id, envelope_hash,
		emitter_agent_id, executor_agent_id, verdict,
		previous_receipt_hash, executor_signature, executor_signed_at,
		emitter_acceptance, emitter_acceptance_at, emitter_signature,
		dispute_reason, data, seq
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	var acceptanceAt *string
	if r.EmitterAcceptanceAt != nil {
		s := r.EmitterAcceptanceAt.Format(time.RFC3339)
		acceptanceAt = &s
	}
	_, err = tx.ExecContext(ctx, query,
		r.ReceiptID, r.ContractID, r.EnvelopeHash,
		r.EmitterAgentID, r.ExecutorAgentID, string(r.Verdict),
		nullable(r.PreviousReceiptHash), r.ExecutorSignature,
		r.ExecutorSignedAt.Format(time.RFC3339),
		nullable(string(r.EmitterAcceptance)),
		acceptanceAt,
		nullable(r.EmitterSignature),
		nullable(r.DisputeReason),
		string(data),
		expectedSeq,
	)
	if err != nil {
		return fmt.Errorf("%w: insert receipt: %v", ErrSequenceConflict, err)
	}
	return tx.Commit()
}

// nullable returns a pointer to the string, or nil if empty.
func nullable(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// GetReceipt retrieves a receipt by its ID.
func (cs *ChainStore) GetReceipt(ctx context.Context, receiptID string) (*SettlementReceipt, error) {
	var data string
	err := cs.db.QueryRowContext(ctx,
		"SELECT data FROM receipts WHERE receipt_id = ?", receiptID).Scan(&data)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrReceiptNotFound
		}
		return nil, err
	}
	var r SettlementReceipt
	if err := json.Unmarshal([]byte(data), &r); err != nil {
		return nil, fmt.Errorf("unmarshal receipt: %w", err)
	}
	return &r, nil
}

// GetChain returns all receipts for an agent pair in chronological order.
func (cs *ChainStore) GetChain(ctx context.Context, emitterID, executorID string) ([]*SettlementReceipt, error) {
	rows, err := cs.db.QueryContext(ctx, `
		SELECT data FROM receipts
		WHERE emitter_agent_id = ? AND executor_agent_id = ?
		ORDER BY seq ASC, executor_signed_at ASC
	`, emitterID, executorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var receipts []*SettlementReceipt
	for rows.Next() {
		var data string
		if err := rows.Scan(&data); err != nil {
			return nil, err
		}
		var r SettlementReceipt
		if err := json.Unmarshal([]byte(data), &r); err != nil {
			return nil, fmt.Errorf("unmarshal receipt: %w", err)
		}
		receipts = append(receipts, &r)
	}
	return receipts, rows.Err()
}

// UpdateReceipt updates a receipt in the chain store.
// It replaces the receipt JSON while preserving the existing chain position
// (prev_hash is not recalculated for an update).
func (cs *ChainStore) UpdateReceipt(ctx context.Context, r *SettlementReceipt) error {
	if r == nil {
		return ErrInvalidReceipt
	}
	if r.ReceiptID == "" || r.ContractID == "" {
		return fmt.Errorf("%w: missing receipt_id or contract_id", ErrInvalidReceipt)
	}
	if err := ValidateReceipt(r); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidReceipt, err)
	}
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal receipt: %w", err)
	}
	result, err := cs.db.ExecContext(ctx,
		`UPDATE receipts SET data = ? WHERE receipt_id = ?`,
		data, r.ReceiptID)
	if err != nil {
		return fmt.Errorf("update: %w", err)
	}
	rows, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("rows affected: %w", err)
	}
	if rows == 0 {
		return ErrReceiptNotFound
	}
	return nil
}

// InjectReceipt inserts or replaces a receipt in the chain store, bypassing the
// normal settlement flow. This is intended ONLY for testing and security validation
// scenarios (e.g., injecting a receipt with an invalid signature to verify that
// VerifyChain correctly detects it).
//
// Unlike SaveReceipt, this uses INSERT OR REPLACE so it can overwrite an existing
// receipt with the same receipt_id. The prev_hash must be provided explicitly;
// it is NOT computed automatically.
func (cs *ChainStore) InjectReceipt(ctx context.Context, r *SettlementReceipt) error {
	if r == nil {
		return ErrInvalidReceipt
	}
	if err := ValidateReceipt(r); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidReceipt, err)
	}
	data, err := json.Marshal(r)
	if err != nil {
		return fmt.Errorf("marshal receipt: %w", err)
	}

	tx, err := cs.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	query := `
	INSERT OR REPLACE INTO receipts (
		receipt_id, contract_id, envelope_hash,
		emitter_agent_id, executor_agent_id, verdict,
		previous_receipt_hash, executor_signature, executor_signed_at,
		emitter_acceptance, emitter_acceptance_at, emitter_signature,
		dispute_reason, data, seq
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`
	var acceptanceAt *string
	if r.EmitterAcceptanceAt != nil {
		s := r.EmitterAcceptanceAt.Format(time.RFC3339)
		acceptanceAt = &s
	}
	_, err = tx.ExecContext(ctx, query,
		r.ReceiptID, r.ContractID, r.EnvelopeHash,
		r.EmitterAgentID, r.ExecutorAgentID, string(r.Verdict),
		nullable(r.PreviousReceiptHash), r.ExecutorSignature,
		nullable(r.ExecutorSignedAt.Format(time.RFC3339)),
		nullable(string(r.EmitterAcceptance)),
		acceptanceAt,
		nullable(r.EmitterSignature),
		nullable(r.DisputeReason),
		string(data),
		r.SequenceNumber,
	)
	if err != nil {
		return fmt.Errorf("inject receipt: %w", err)
	}
	return tx.Commit()
}

// GetLastReceipt returns the most recent receipt for an agent pair.
func (cs *ChainStore) GetLastReceipt(ctx context.Context, emitterID, executorID string) (*SettlementReceipt, error) {
	var data string
	err := cs.db.QueryRowContext(ctx, `
		SELECT data FROM receipts
		WHERE emitter_agent_id = ? AND executor_agent_id = ?
		ORDER BY seq DESC, executor_signed_at DESC
		LIMIT 1
	`, emitterID, executorID).Scan(&data)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrReceiptNotFound
		}
		return nil, err
	}
	var r SettlementReceipt
	if err := json.Unmarshal([]byte(data), &r); err != nil {
		return nil, fmt.Errorf("unmarshal receipt: %w", err)
	}
	return &r, nil
}

// VerifyChain cryptographically and sequentially verifies the entire receipt chain for an agent pair.
func (cs *ChainStore) VerifyChain(
	ctx context.Context,
	emitterID, executorID string,
	executorPublicKey, emitterPublicKey []byte,
) ([]VerificationResult, error) {
	chain, err := cs.GetChain(ctx, emitterID, executorID)
	if err != nil {
		return nil, err
	}
	return VerifyChainIntegrity(chain, executorPublicKey, emitterPublicKey)
}

// VerifyChainIntegrity cryptographically and sequentially verifies a receipt chain.
// It checks for each receipt:
//   - Sequence number is strictly monotonic starting at 1 (detects gaps and reordering)
//   - previous_receipt_hash == SHA-256(executor_signature of previous receipt) (empty for first)
//   - Executor signature is valid against executorPublicKey (detects content modification)
//   - Emitter acceptance signature is valid against emitterPublicKey (if present)
func VerifyChainIntegrity(
	chain []*SettlementReceipt,
	executorPublicKey, emitterPublicKey []byte,
) ([]VerificationResult, error) {
	if len(chain) == 0 {
		return nil, ErrReceiptNotFound
	}

	results := make([]VerificationResult, 0, len(chain))

	for i, r := range chain {
		result := VerificationResult{
			ReceiptID:      r.ReceiptID,
			ContractID:     r.ContractID,
			Verdict:        r.Verdict,
			Index:          i,
			SequenceNumber: r.SequenceNumber,
		}

		// 1. Verify sequence monotonicity (1, 2, 3...)
		expectedSeq := int64(i + 1)
		if r.SequenceNumber != expectedSeq {
			result.SequenceValid = false
			if r.SequenceNumber > expectedSeq {
				result.Error = fmt.Sprintf("sequence gap detected at index %d: expected %d, got %d (receipt deleted)", i, expectedSeq, r.SequenceNumber)
			} else {
				result.Error = fmt.Sprintf("sequence out of order at index %d: expected %d, got %d", i, expectedSeq, r.SequenceNumber)
			}
		} else {
			result.SequenceValid = true
		}

		// 2. Verify chain link (previous_receipt_hash).
		if i == 0 {
			result.PreviousHashValid = (r.PreviousReceiptHash == "")
			if !result.PreviousHashValid && result.Error == "" {
				result.Error = "first receipt must have empty previous_receipt_hash"
			}
		} else {
			prevSig := chain[i-1].ExecutorSignature
			h := sha256.Sum256([]byte(prevSig))
			hash := hex.EncodeToString(h[:])
			result.PreviousHashValid = (hash == r.PreviousReceiptHash)
			if !result.PreviousHashValid && result.Error == "" {
				result.Error = fmt.Sprintf("chain broken: expected SHA-256(prev_sig)=%s, got %s", hash, r.PreviousReceiptHash)
			}
		}

		// 3. Verify executor signature over receipt content.
		if r.ExecutorSignature != "" {
			receiptHash, err := ComputeReceiptHash(r)
			if err != nil {
				result.ExecutorSignatureValid = false
				if result.Error == "" {
					result.Error = fmt.Sprintf("compute receipt hash: %v", err)
				}
			} else {
				err := signing.Verify(executorPublicKey, []byte(receiptHash), r.ExecutorSignature)
				result.ExecutorSignatureValid = (err == nil)
				if err != nil && result.Error == "" {
					result.Error = fmt.Sprintf("executor signature invalid: %v", err)
				}
			}
		} else {
			result.ExecutorSignatureValid = false
			if result.Error == "" {
				result.Error = "missing executor signature"
			}
		}

		// 4. Verify emitter acceptance/dispute signature (if present).
		switch {
		case r.EmitterSignature != "" && r.EmitterAcceptance != "":
			receiptHash, err := ComputeReceiptHash(r)
			if err != nil {
				result.EmitterSignatureValid = false
				if result.Error == "" {
					result.Error = fmt.Sprintf("compute receipt hash for emitter: %v", err)
				}
			} else {
				err := signing.Verify(emitterPublicKey, []byte(receiptHash), r.EmitterSignature)
				result.EmitterSignatureValid = (err == nil)
				if err != nil && result.Error == "" {
					result.Error = fmt.Sprintf("emitter signature invalid: %v", err)
				}
			}
		case r.EmitterSignature == "" && r.EmitterAcceptance != "":
			// Receipt emitted but not yet accepted/disputed by A.
			result.EmitterSignatureValid = true
		default:
			// No emitter action yet (receipt not yet reviewed by A).
			result.EmitterSignatureValid = true
		}

		result.Valid = result.SequenceValid &&
			result.PreviousHashValid &&
			result.ExecutorSignatureValid &&
			result.EmitterSignatureValid

		results = append(results, result)
	}

	return results, nil
}

// VerificationResult is the result of verifying a single receipt in the chain.
type VerificationResult struct {
	ReceiptID              string
	ContractID             string
	Verdict                Verdict
	Index                  int
	SequenceNumber         int64
	PreviousHashValid      bool
	SequenceValid          bool
	ExecutorSignatureValid bool
	EmitterSignatureValid  bool
	Valid                  bool
	Error                  string
}

// ComputeReceiptHash computes the JCS canonical SHA-256 hash of a receipt
// with all mutable fields cleared: both signatures, emitter acceptance,
// acceptance timestamp, and dispute reason.
//
// This represents the EXECUTOR-signed content: the immutable receipt data
// plus assertions, as signed by the executor agent. The emitter signs this
// exact same content (via AcceptReceipt/DisputeReceipt) so that verification
// is deterministic regardless of whether the emitter has responded yet.
func ComputeReceiptHash(r *SettlementReceipt) (string, error) {
	cleared := *r
	cleared.ExecutorSignature = ""
	cleared.EmitterAcceptance = ""
	cleared.EmitterAcceptanceAt = nil
	cleared.EmitterSignature = ""
	cleared.DisputeReason = ""
	cleared.PreviousReceiptHash = ""
	cleared.SequenceNumber = 0

	data, err := jcs.Marshal(&cleared)
	if err != nil {
		return "", fmt.Errorf("jcs marshal: %w", err)
	}
	return jcs.HashHex(data)
}

// ─────────────────────────────────────────────────────────────────
// Internal helpers
// ─────────────────────────────────────────────────────────────────

// Count returns the total number of receipts for a pair.
func (cs *ChainStore) Count(ctx context.Context, emitterID, executorID string) (int, error) {
	var n int
	err := cs.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM receipts
		WHERE emitter_agent_id = ? AND executor_agent_id = ?
	`, emitterID, executorID).Scan(&n)
	return n, err
}

// DeleteAll removes all receipts for a pair. Use only in tests.
func (cs *ChainStore) DeleteAll(ctx context.Context, emitterID, executorID string) error {
	_, err := cs.db.ExecContext(ctx, `
		DELETE FROM receipts
		WHERE emitter_agent_id = ? AND executor_agent_id = ?
	`, emitterID, executorID)
	return err
}
