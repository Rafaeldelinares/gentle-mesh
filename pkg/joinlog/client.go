package joinlog

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

var (
	// ErrInvalidSnapshotDigest is returned when a snapshot's state_digest does not match
	// the SHA-256 of its canonical JCS representation.
	ErrInvalidSnapshotDigest = errors.New("invalid snapshot state digest: verification failed")

	// ErrChainDivergence is returned when the coordinator rejects event subscription
	// with HTTP 409 Conflict because the client's anchor hash does not match the server's chain.
	ErrChainDivergence = errors.New("chain divergence detected by coordinator (HTTP 409)")
)

// SubscriberConfig holds configuration for the SituatedSubscriber.
type SubscriberConfig struct {
	CoordinatorURL string
	Token          string
	HTTPClient     *http.Client
}

// SituatedSubscriber manages fetching the initial verified mesh snapshot and
// maintaining continuous situated awareness via the SSE events stream (RFC-003 J3/J4).
type SituatedSubscriber struct {
	config     SubscriberConfig
	httpClient *http.Client
}

// NewSituatedSubscriber creates a new SituatedSubscriber.
func NewSituatedSubscriber(cfg SubscriberConfig) *SituatedSubscriber {
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &SituatedSubscriber{
		config:     cfg,
		httpClient: client,
	}
}

// CatchUp fetches GET /v1/mesh/snapshot and independently verifies its state_digest (RFC-003 J3).
func (s *SituatedSubscriber) CatchUp(ctx context.Context) (*Snapshot, error) {
	url := strings.TrimRight(s.config.CoordinatorURL, "/") + "/v1/mesh/snapshot"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("create snapshot request: %w", err)
	}
	if s.config.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.config.Token)
	}

	resp, err := s.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch snapshot: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("fetch snapshot returned status %d: %s", resp.StatusCode, string(body))
	}

	var snap Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		return nil, fmt.Errorf("decode snapshot: %w", err)
	}

	valid, err := VerifySnapshot(&snap)
	if err != nil {
		return nil, fmt.Errorf("verify snapshot digest: %w", err)
	}
	if !valid {
		return nil, ErrInvalidSnapshotDigest
	}

	return &snap, nil
}

// SubscribeEvents connects to GET /v1/mesh/events using Last-Event-ID = "<sinceSeq>:<sinceHash>".
// If the coordinator detects chain divergence, it returns HTTP 409 Conflict, causing
// SubscribeEvents to abort immediately with ErrChainDivergence.
func (s *SituatedSubscriber) SubscribeEvents(ctx context.Context, sinceSeq int64, sinceHash string, onEvent func(Event)) error {
	url := strings.TrimRight(s.config.CoordinatorURL, "/") + "/v1/mesh/events"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("create events request: %w", err)
	}
	if s.config.Token != "" {
		req.Header.Set("Authorization", "Bearer "+s.config.Token)
	}

	// Anchor cursor with Last-Event-ID header
	if sinceSeq > 0 || sinceHash != "" {
		if sinceHash != "" {
			req.Header.Set("Last-Event-ID", fmt.Sprintf("%d:%s", sinceSeq, sinceHash))
		} else {
			req.Header.Set("Last-Event-ID", fmt.Sprintf("%d", sinceSeq))
		}
	}

	// We use an HTTP client without a global timeout for streaming SSE
	streamClient := s.httpClient
	if streamClient.Timeout > 0 {
		// Clone transport to avoid stream cancellation by the request timeout
		streamClient = &http.Client{
			Transport: streamClient.Transport,
		}
	}

	resp, err := streamClient.Do(req)
	if err != nil {
		return fmt.Errorf("connect to events stream: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%w: %s", ErrChainDivergence, string(body))
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("events stream returned status %d: %s", resp.StatusCode, string(body))
	}

	reader := bufio.NewReader(resp.Body)
	var currentData strings.Builder

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) || errors.Is(ctx.Err(), context.Canceled) {
				return nil
			}
			return fmt.Errorf("read stream line: %w", err)
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			// Dispatch event if data collected
			if currentData.Len() > 0 {
				var evt Event
				if err := json.Unmarshal([]byte(currentData.String()), &evt); err == nil && onEvent != nil {
					onEvent(evt)
				}
				currentData.Reset()
			}
			continue
		}

		if strings.HasPrefix(line, ":") {
			// SSE comment (heartbeat ping)
			continue
		}

		if strings.HasPrefix(line, "data:") {
			data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
			currentData.WriteString(data)
		}
	}
}
