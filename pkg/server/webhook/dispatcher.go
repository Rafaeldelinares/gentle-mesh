package webhook

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/store"
)

// Dispatcher sends webhook notifications for task events.
type Dispatcher struct {
	store    store.WebhookStore
	client   *http.Client
	queue    chan *Notification
	stopCh   chan struct{}
}

// Notification represents a webhook notification payload.
type Notification struct {
	ID        string            `json:"id"`
	Event     string            `json:"event"`
	Timestamp int64            `json:"timestamp"`
	TaskID    string           `json:"task_id"`
	Status    string           `json:"status"`
	Error     *string          `json:"error,omitempty"`
	Result    *string          `json:"result,omitempty"`
	Task      *protocol.TaskState `json:"task,omitempty"`
}

// NewDispatcher creates a new webhook dispatcher.
func NewDispatcher(webhookStore store.WebhookStore) *Dispatcher {
	d := &Dispatcher{
		store:  webhookStore,
		client: &http.Client{Timeout: 10 * time.Second},
		queue:  make(chan *Notification, 100),
		stopCh: make(chan struct{}),
	}
	go d.processLoop()
	return d
}

// processLoop processes notifications from the queue.
func (d *Dispatcher) processLoop() {
	for {
		select {
		case <-d.stopCh:
			return
		case notif := <-d.queue:
			d.sendToWebhooks(notif)
		}
	}
}

// sendToWebhooks finds matching webhooks and sends notifications.
func (d *Dispatcher) sendToWebhooks(notif *Notification) {
	ctx := context.Background()
	webhooks, err := d.store.ListActiveWebhooks(ctx, notif.Event)
	if err != nil {
		log.Printf("webhook dispatcher: failed to list webhooks: %v", err)
		return
	}

	for _, wh := range webhooks {
		go d.sendWebhook(wh, notif)
	}
}

// sendWebhook sends a single notification to a webhook URL.
func (d *Dispatcher) sendWebhook(wh *store.WebhookRecord, notif *Notification) {
	payload, err := json.Marshal(notif)
	if err != nil {
		log.Printf("webhook dispatcher: failed to marshal payload: %v", err)
		return
	}

	req, err := http.NewRequest(http.MethodPost, wh.URL, bytes.NewReader(payload))
	if err != nil {
		log.Printf("webhook dispatcher: failed to create request: %v", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Webhook-Event", notif.Event)
	req.Header.Set("X-Webhook-ID", notif.ID)

	// Sign the payload if secret is configured
	if wh.Secret != "" {
		signature := d.signPayload(payload, wh.Secret)
		req.Header.Set("X-Webhook-Signature", signature)
	}

	resp, err := d.client.Do(req)
	if err != nil {
		log.Printf("webhook dispatcher: failed to send to %s: %v", wh.URL, err)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		log.Printf("webhook dispatcher: webhook %s returned %d: %s", wh.URL, resp.StatusCode, string(body))
	}
}

// signPayload creates an HMAC-SHA256 signature of the payload.
func (d *Dispatcher) signPayload(payload []byte, secret string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write(payload)
	return "sha256=" + hex.EncodeToString(h.Sum(nil))
}

// Notify queues a notification for delivery.
func (d *Dispatcher) Notify(event string, taskID string, status string, task *protocol.TaskState, result *string, err *string) {
	notif := &Notification{
		ID:        fmt.Sprintf("notif-%d", time.Now().UnixNano()),
		Event:     event,
		Timestamp: time.Now().Unix(),
		TaskID:    taskID,
		Status:    status,
		Error:     err,
		Result:    result,
		Task:      task,
	}

	select {
	case d.queue <- notif:
	default:
		log.Printf("webhook dispatcher: queue full, dropping notification for task %s", taskID)
	}
}

// NotifyTaskCompleted is a convenience method for task completion events.
func (d *Dispatcher) NotifyTaskCompleted(task *protocol.TaskState, result string) {
	d.Notify("task.completed", task.TaskID, "completed", task, &result, nil)
}

// NotifyTaskFailed is a convenience method for task failure events.
func (d *Dispatcher) NotifyTaskFailed(task *protocol.TaskState, errMsg string) {
	d.Notify("task.failed", task.TaskID, "failed", task, nil, &errMsg)
}

// NotifyTaskTimeout is a convenience method for task timeout events.
func (d *Dispatcher) NotifyTaskTimeout(task *protocol.TaskState, errMsg string) {
	d.Notify("task.timeout", task.TaskID, "timeout", task, nil, &errMsg)
}

// Stop gracefully shuts down the dispatcher.
func (d *Dispatcher) Stop() {
	close(d.stopCh)
}
