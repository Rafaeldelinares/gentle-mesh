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
	ErrChainBroken            = errors.New("receipt chain is broken")
	ErrChainVerificationFailed = errors.New("chain verification failed")
	ErrInvalidReceipt         = errors.New("invalid receipt")
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
			receipt_id             TEXT NOT NULL,
		contract_id            TEXT NOT NULL,
		envelope_hash          TEXT NOT NULL,
		emitter_agent_id       TEXT NOT NULL,
		executor_agent_id     TEXT NOT NULL,
		verdict               TEXT NOT NULL,
		previous_receipt_hash  TEXT,
		executor_signature     TEXT NOT NULL,
		executor_signed_at     TEXT NOT NULL,
		emitter_acceptance    TEXT,
		emitter_acceptance_at TEXT,
		emitter_signature      TEXT,
		dispute_reason        TEXT,
		data                  TEXT NOT NULL
	);

	CREATE INDEX IF NOT EXISTS idx_receipts_pair
		ON receipts(emitter_agent_id, executor_agent_id, executor_signed_at);

	CREATE INDEX IF NOT EXISTS idx_receipts_contract
		ON receipts(contract_id);
	`
	_, err := cs.db.ExecContext(ctx, schema)
	return err
}

// SaveReceipt persists a receipt to the chain atomically using a SQLite transaction.
// Chain integrity (prev_hash validation) is NOT done here — VerifyChain
// validates chain structure independently. Application-level validation of
// prev_hash in SaveReceipt is inherently incompatible with concurrent writes
// because concurrent goroutines all read the same last receipt and compute
// the same hash, but the DB state changes between read and validation.
const maxSaveRetries = 3

func (cs *ChainStore) SaveReceipt(ctx context.Context, r *SettlementReceipt) error {
	if r == nil {
		return ErrInvalidReceipt
	}

	cs.mu.Lock()
	defer cs.mu.Unlock()

	// If prev_hash is empty, compute it from the last receipt inside the critical
	// section. This ensures that concurrent goroutines each see the correct
	// previous receipt and compute distinct prev_hash values.
	if r.PreviousReceiptHash == "" {
		last, err := cs.getLastReceiptUnlocked(ctx, r.EmitterAgentID, r.ExecutorAgentID)
		if err != nil && !errors.Is(err, ErrReceiptNotFound) {
			return fmt.Errorf("get last receipt: %w", err)
		}
		if last != nil {
			h := sha256.Sum256([]byte(last.ExecutorSignature))
			r.PreviousReceiptHash = hex.EncodeToString(h[:])
		}
	}

	var lastErr error
	for attempt := 0; attempt < maxSaveRetries; attempt++ {
		err := cs.saveReceiptOnce(ctx, r)
		if err == nil {
			return nil
		}
		lastErr = err
		if attempt < maxSaveRetries-1 {
			time.Sleep(time.Millisecond * 5 * time.Duration(attempt+1))
		}
	}
	return fmt.Errorf("save receipt after %d attempts: %w", maxSaveRetries, lastErr)
}

// getLastReceiptUnlocked returns the last receipt for a pair. Caller must hold mu.
func (cs *ChainStore) getLastReceiptUnlocked(ctx context.Context, emitterID, executorID string) (*SettlementReceipt, error) {
	var data string
	err := cs.db.QueryRowContext(ctx,
		`SELECT data FROM receipts
		 WHERE emitter_agent_id = ? AND executor_agent_id = ?
		 ORDER BY executor_signed_at DESC LIMIT 1`,
		emitterID, executorID).Scan(&data)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrReceiptNotFound
		}
		return nil, fmt.Errorf("query last receipt: %w", err)
	}
	var r SettlementReceipt
	if err := json.Unmarshal([]byte(data), &r); err != nil {
		return nil, fmt.Errorf("unmarshal receipt: %w", err)
	}
	return &r, nil
}

// saveReceiptOnce inserts a receipt inside a transaction (no prev_hash validation).
func (cs *ChainStore) saveReceiptOnce(ctx context.Context, r *SettlementReceipt) error {
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
	INSERT INTO receipts (
		receipt_id, contract_id, envelope_hash,
		emitter_agent_id, executor_agent_id, verdict,
		previous_receipt_hash, executor_signature, executor_signed_at,
		emitter_acceptance, emitter_acceptance_at, emitter_signature,
		dispute_reason, data
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
	)
	if err != nil {
		return fmt.Errorf("insert receipt: %w", err)
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
		ORDER BY executor_signed_at ASC
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
		return errors.New("receipt is nil")
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

// GetLastReceipt returns the most recent receipt for an agent pair.
func (cs *ChainStore) GetLastReceipt(ctx context.Context, emitterID, executorID string) (*SettlementReceipt, error) {
	var data string
	err := cs.db.QueryRowContext(ctx, `
		SELECT data FROM receipts
		WHERE emitter_agent_id = ? AND executor_agent_id = ?
		ORDER BY executor_signed_at DESC
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

// VerifyChain cryptographically verifies the entire receipt chain.
// It checks for each receipt:
//   - previous_receipt_hash == SHA-256(executor_signature of previous receipt)
//   - Executor signature is valid (using executorPublicKey)
//   - Emitter acceptance signature is valid (using emitterPublicKey, if present)
func (cs *ChainStore) VerifyChain(
	ctx context.Context,
	emitterID, executorID string,
	executorPublicKey, emitterPublicKey []byte,
) ([]VerificationResult, error) {
	chain, err := cs.GetChain(ctx, emitterID, executorID)
	if err != nil {
		return nil, err
	}
	if len(chain) == 0 {
		return nil, ErrReceiptNotFound
	}

	results := make([]VerificationResult, 0, len(chain))

	for i, r := range chain {
		result := VerificationResult{
			ReceiptID:   r.ReceiptID,
			ContractID:  r.ContractID,
			Verdict:    r.Verdict,
			Index:      i,
		}

		// 1. Verify chain link (previous_receipt_hash).
		if i == 0 {
			result.PreviousHashValid = r.PreviousReceiptHash == ""
			if !result.PreviousHashValid {
				result.Error = "first receipt must have empty previous_receipt_hash"
			}
		} else {
			prevSig := chain[i-1].ExecutorSignature
			h := sha256.Sum256([]byte(prevSig))
			hash := hex.EncodeToString(h[:])
			result.PreviousHashValid = (hash == r.PreviousReceiptHash)
			if !result.PreviousHashValid {
				result.Error = fmt.Sprintf("chain broken: expected SHA-256(prev_sig)=%s, got %s",
					hash, r.PreviousReceiptHash)
			}
		}

		// 2. Verify executor signature over receipt content.
		if r.ExecutorSignature != "" {
			receiptHash, err := ComputeReceiptHash(r)
			if err != nil {
				result.Error = fmt.Sprintf("compute receipt hash: %v", err)
			} else {
				err := signing.Verify(executorPublicKey, []byte(receiptHash), r.ExecutorSignature)
				result.ExecutorSignatureValid = (err == nil)
				if err != nil {
					result.Error = fmt.Sprintf("executor signature invalid: %v", err)
				}
			}
		} else {
			result.ExecutorSignatureValid = false
			result.Error = "missing executor signature"
		}

		// 3. Verify emitter acceptance/dispute signature (if present).
		switch {
		case r.EmitterSignature != "" && r.EmitterAcceptance != "":
			receiptHash, err := ComputeReceiptHash(r)
			if err != nil {
				result.Error = fmt.Sprintf("compute receipt hash for emitter: %v", err)
			} else {
				err := signing.Verify(emitterPublicKey, []byte(receiptHash), r.EmitterSignature)
				result.EmitterSignatureValid = (err == nil)
				if err != nil {
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

		result.Valid = result.PreviousHashValid &&
			result.ExecutorSignatureValid &&
			result.EmitterSignatureValid

		results = append(results, result)
	}

	return results, nil
}

// VerificationResult is the result of verifying a single receipt in the chain.
type VerificationResult struct {
	ReceiptID              string
	ContractID            string
	Verdict              Verdict
	Index                int
	PreviousHashValid    bool
	ExecutorSignatureValid bool
	EmitterSignatureValid  bool
	Valid                 bool
	Error                string
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

	data, err := jcs.Marshal(&cleared)
	if err != nil {
		return "", fmt.Errorf("jcs marshal: %w", err)
	}
	return jcs.HashHex(data)
}

// ─────────────────────────────────────────────────────────────────
// Internal helpers
// ─────────────────────────────────────────────────────────────────

// lastExecutorSignatureRaw returns the base64url-encoded executor signature
// of the most recent receipt in the pair's chain.
func (cs *ChainStore) lastExecutorSignatureRaw(
	ctx context.Context, emitterID, executorID string,
) (string, error) {
	var sig string
	err := cs.db.QueryRowContext(ctx, `
		SELECT executor_signature FROM receipts
		WHERE emitter_agent_id = ? AND executor_agent_id = ?
		ORDER BY executor_signed_at DESC
		LIMIT 1
	`, emitterID, executorID).Scan(&sig)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", ErrReceiptNotFound
		}
		return "", err
	}
	return sig, nil
}

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
