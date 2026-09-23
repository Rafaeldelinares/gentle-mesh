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

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
)

var errStreamDone = errors.New("sse stream finished")

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

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("remote worker %s returned status %d: %s", r.endpoint, resp.StatusCode, string(body))
	}

	reader := bufio.NewReader(resp.Body)
	var blockLines []string

	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil && readErr != io.EOF {
			return readErr
		}

		cleanLine := strings.TrimRight(line, "\r\n")
		if cleanLine == "" {
			if len(blockLines) > 0 {
				if err := r.processBlock(ctx, blockLines, sink); err != nil {
					if errors.Is(err, errStreamDone) {
						return nil
					}
					return err
				}
				blockLines = blockLines[:0]
			}
		} else {
			blockLines = append(blockLines, cleanLine)
		}

		if readErr == io.EOF {
			if len(blockLines) > 0 {
				if err := r.processBlock(ctx, blockLines, sink); err != nil && !errors.Is(err, errStreamDone) {
					return err
				}
			}
			break
		}
	}

	return nil
}

func (r *RemoteRunner) processBlock(ctx context.Context, lines []string, sink EventSink) error {
	hasContent := false
	for _, l := range lines {
		trimmed := strings.TrimSpace(l)
		if trimmed != "" && !strings.HasPrefix(trimmed, ":") {
			hasContent = true
			break
		}
	}
	if !hasContent {
		return nil
	}

	raw := []byte(strings.Join(lines, "\n"))
	evt, err := protocol.ParseSSE(raw)
	if err != nil {
		return fmt.Errorf("failed to parse sse event: %w", err)
	}

	if _, err := sink.EmitEvent(evt.Type, evt.Payload); err != nil {
		return fmt.Errorf("failed to emit event: %w", err)
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
		return errStreamDone
	}

	return nil
}
