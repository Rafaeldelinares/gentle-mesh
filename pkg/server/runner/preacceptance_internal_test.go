package runner

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
)

// discardSink swallows every event: this test only cares about the transport error.
type discardSink struct{}

func (discardSink) Context() context.Context           { return context.Background() }
func (discardSink) RegisterQuery(string) <-chan string { return nil }
func (discardSink) ResolveQuery(string, string)        {}
func (discardSink) EmitEvent(protocol.EventType, any) (protocol.Event, error) {
	return protocol.Event{}, nil
}

// The error a real RemoteRunner.Run produces when nobody is listening must be recognized as
// pre-acceptance: the whole retry depends on it. This prints the real error type and message.
func TestIsPreAcceptanceTransportErrorOnARealDialFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to reserve a port: %v", err)
	}
	endpoint := "http://" + ln.Addr().String()
	_ = ln.Close()

	remote := NewRemoteRunner(RemoteRunnerConfig{Endpoint: endpoint, Client: &http.Client{Timeout: 5 * time.Second}})
	runErr := remote.Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "run"}, discardSink{})
	if runErr == nil {
		t.Fatal("expected a transport error from an endpoint nobody is listening on")
	}
	t.Logf("error real de RemoteRunner.Run: tipo=%T mensaje=%q", runErr, runErr.Error())
	if !isPreAcceptanceTransportError(runErr) {
		t.Errorf("the real dial error must be classified as pre-acceptance, got %T: %v", runErr, runErr)
	}
}
