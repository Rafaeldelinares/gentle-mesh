package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Rafaeldelinares/gentle-mesh/pkg/protocol"
)

var errStreamDone = errors.New("sse stream finished")

// ErrNodeLost is returned when a remote node's event stream ends without a terminal event
// (completion or error). The node accepted the task and then vanished, so the run must not be
// reported as a success: the task is finalized with an explicit cause instead of staying running
// forever.
var ErrNodeLost = errors.New("node lost: stream ended without a result")

// ErrNodeBusy is returned when a remote worker rejects a task with HTTP 429 Too Many Requests.
// The node is healthy and online, but its active concurrency limit and queue capacity are full.
type ErrNodeBusy struct {
	Endpoint   string
	StatusCode int
	Body       string
}

func (e *ErrNodeBusy) Error() string {
	return fmt.Sprintf("remote worker %s returned status %d: %s", e.Endpoint, e.StatusCode, e.Body)
}

// IsNodeBusyError reports whether err was caused by a worker returning HTTP 429 Too Many Requests.
func IsNodeBusyError(err error) bool {
	var busyErr *ErrNodeBusy
	return errors.As(err, &busyErr)
}

// RemoteRunnerConfig holds connection and authentication parameters for a remote worker node.
type RemoteRunnerConfig struct {
	Endpoint string
	Client   *http.Client
	Token    string
}

// RemoteRunner implements the Runner interface by executing tasks on a remote gentle-mesh worker node.
type RemoteRunner struct {
	endpoint string
	client   *http.Client
	token    string
}

// NewRemoteRunner instantiates a new RemoteRunner.
func NewRemoteRunner(cfg RemoteRunnerConfig) *RemoteRunner {
	client := cfg.Client
	if client == nil {
		client = http.DefaultClient
	}
	return &RemoteRunner{
		endpoint: cfg.Endpoint,
		client:   client,
		token:    cfg.Token,
	}
}

// Run dispatches a TaskRequest to the remote worker's /v1/execute endpoint and streams back events.
func (r *RemoteRunner) Run(ctx context.Context, req protocol.TaskRequest, sink EventSink) error {
	if sink == nil {
		return errors.New("event sink cannot be nil")
	}

	bodyBytes, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("failed to marshal task request: %w", err)
	}

	executeURL := strings.TrimRight(r.endpoint, "/") + "/v1/execute"
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, executeURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return fmt.Errorf("failed to create execute request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	if r.token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+r.token)
	}

	resp, err := r.client.Do(httpReq)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		body, _ := io.ReadAll(resp.Body)
		return &ErrNodeBusy{
			Endpoint:   r.endpoint,
			StatusCode: resp.StatusCode,
			Body:       string(body),
		}
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("remote worker %s returned status %d: %s", r.endpoint, resp.StatusCode, string(body))
	}

	reader := bufio.NewReader(resp.Body)
	var blockLines []string
	// sawEvent records whether the worker forwarded at least one real event to the sink
	// before the stream went away. It is the only signal the loop has to distinguish a
	// pre-acceptance transport error (the worker died before/while we read anything) from
	// a post-acceptance loss (the worker accepted the task — it emitted at least status:running
	// — and then its connection was reset, half-closed or torn down without a terminal event).
	// The first case is a normal pre-acceptance failure that the MeshRunner must retry on
	// another node; the second is a lost node and must be classified as NODE_LOST, not retried.
	sawEvent := false

	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil && readErr != io.EOF {
			// RST (kill -9), io.ErrUnexpectedEOF, or a *net.OpError all look like this: the
			// node's socket was torn down while we were reading the body. First, try to flush
			// any in-flight block — a worker killed mid-block may have left a complete event
			// sitting in blockLines without the closing empty line. Then, if we had any real
			// event from the node, it had accepted the task: treat the loss like io.EOF does,
			// so the scheduler classifies it as NODE_LOST. Otherwise this is a pre-acceptance
			// transport error and the MeshRunner may retry on another node.
			if len(blockLines) > 0 {
				terminal, emitted, err := r.processBlock(ctx, blockLines, sink)
				if err != nil && !errors.Is(err, errStreamDone) {
					return err
				}
				if terminal || errors.Is(err, errStreamDone) {
					return nil
				}
				if emitted {
					sawEvent = true
				}
			}
			if sawEvent {
				return ErrNodeLost
			}
			return readErr
		}

		cleanLine := strings.TrimRight(line, "\r\n")
		if cleanLine == "" {
			if len(blockLines) > 0 {
				terminal, emitted, err := r.processBlock(ctx, blockLines, sink)
				if err != nil {
					if errors.Is(err, errStreamDone) {
						return nil
					}
					return err
				}
				if terminal {
					return nil
				}
				if emitted {
					sawEvent = true
				}
				blockLines = blockLines[:0]
			}
		} else {
			blockLines = append(blockLines, cleanLine)
		}

		if readErr == io.EOF {
			if len(blockLines) > 0 {
				terminal, emitted, err := r.processBlock(ctx, blockLines, sink)
				if err != nil && !errors.Is(err, errStreamDone) {
					return err
				}
				if terminal || errors.Is(err, errStreamDone) {
					return nil
				}
				if emitted {
					sawEvent = true
				}
			}
			// The stream ended without completion or error: the node accepted the task and then
			// disappeared, so this is a lost node, not a success.
			return ErrNodeLost
		}
	}
}

// processBlock forwards one SSE block and reports whether it was a terminal event
// and whether it actually emitted anything to the sink. emitted distinguishes a block
// with real content (the worker spoke) from one that only carried SSE comments (the
// worker sent `:keep-alive`-style heartbeats, which ParseSSE skips). The caller uses
// emitted to know whether the worker had made progress before the stream went away.
func (r *RemoteRunner) processBlock(ctx context.Context, lines []string, sink EventSink) (terminal, emitted bool, err error) {
	hasContent := false
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed != "" && !strings.HasPrefix(trimmed, ":") {
			hasContent = true
			break
		}
	}
	if !hasContent {
		return false, false, nil
	}

	raw := []byte(strings.Join(lines, "\n"))
	evt, err := protocol.ParseSSE(raw)
	if err != nil {
		return false, false, fmt.Errorf("failed to parse sse event: %w", err)
	}

	if _, err := sink.EmitEvent(evt.Type, evt.Payload); err != nil {
		return false, false, fmt.Errorf("failed to emit event: %w", err)
	}

	if evt.Type == protocol.EventQuery {
		var qp protocol.QueryPayload
		if err := evt.UnmarshalPayload(&qp); err == nil && qp.QueryID != "" {
			go func(qID string) {
				answerChan := sink.RegisterQuery(qID)
				select {
				case <-ctx.Done():
					return
				case answer, ok := <-answerChan:
					if !ok || answer == "" {
						return
					}
					// Send POST to r.endpoint + "/v1/reply" with protocol.TaskReplyRequest
					replyReq := protocol.TaskReplyRequest{QueryID: qID, Answer: answer}
					replyBytes, _ := json.Marshal(replyReq)
					replyHTTP, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(r.endpoint, "/")+"/v1/reply", bytes.NewReader(replyBytes))
					if err == nil {
						replyHTTP.Header.Set("Content-Type", "application/json")
						if r.token != "" {
							replyHTTP.Header.Set("Authorization", "Bearer "+r.token)
						}
						if resp, err := r.client.Do(replyHTTP); err == nil {
							resp.Body.Close()
						}
					}
				}
			}(qp.QueryID)
		}
	}

	if evt.Type == protocol.EventCompletion || evt.Type == protocol.EventError {
		return true, true, errStreamDone
	}

	return false, true, nil
}
