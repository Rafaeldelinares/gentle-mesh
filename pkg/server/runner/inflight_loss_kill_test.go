package runner

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
)

// TestRunReturnsNodeLostWhenTheStreamIsReset pins the bug exposed by
// e2e/fix-inflight-e2e.sh: when a worker is killed with SIGKILL (kill -9),
// the kernel closes the socket without sending a TCP FIN, so the remote
// reader sees io.ErrUnexpectedEOF (or an *net.OpError wrapping it), not
// io.EOF. The current code at pkg/server/runner/remote.go returns the raw
// read error in that case, which the scheduler classifies as
// DISPATCH_FAILED instead of NODE_LOST — the opposite of what finding 29
// (PR #97, ce402e3) requires.
//
// Reproduction: a handler that emits status:running (which attributes the
// task) and then RST-closes the socket before the SSE stream terminates.
// The remote reader hits io.ErrUnexpectedEOF on the next read.
//
// Expected after fix: RemoteRunner.Run returns ErrNodeLost (because the
// node accepted the task — it emitted status:running — and then vanished
// without a terminal event).
func TestRunReturnsNodeLostWhenTheStreamIsReset(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("ResponseWriter is not a Hijacker")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			return
		}
		// Descarta el body del POST.
		go func() { _, _ = conn.Read(make([]byte, 4096)) }()
		time.Sleep(20 * time.Millisecond)
		// Envia el response SSE incompleto (sin el `\n\n` final) y un
		// evento status:running que atribuye la tarea.
		resp := "HTTP/1.1 200 OK\r\n" +
			"Content-Type: text/event-stream\r\n" +
			"Cache-Control: no-cache\r\n" +
			"\r\n" +
			"event: status\n" +
			"data: {\"id\":1,\"task_id\":\"t\",\"timestamp\":0,\"type\":\"status\",\"payload\":{\"status\":\"running\"}}\n"
		_, _ = conn.Write([]byte(resp))
		// SO_LINGER=0 fuerza un RST al cerrar, igual que kill -9.
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetLinger(0)
		}
		_ = conn.Close()
	}))
	defer srv.Close()

	tr := &http.Transport{ResponseHeaderTimeout: 2 * time.Second}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}

	sink := &capturingSink{}
	err := NewRemoteRunner(RemoteRunnerConfig{
		Endpoint: srv.URL,
		Client:   client,
	}).Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "x"}, sink)

	if !errors.Is(err, ErrNodeLost) {
		t.Fatalf("Run must return ErrNodeLost when the stream is reset after accepting the task, got err=%v (%T)", err, err)
	}
	if len(sink.events) != 1 || sink.events[0] != protocol.EventStatus {
		t.Fatalf("the status:running event must still be forwarded before the reset, got events=%v", sink.events)
	}
}

// TestRunReturnsRawPreAcceptanceErrorWhenTheStreamIsResetBeforeAnyEvent
// is the negative of TestRunReturnsNodeLostWhenTheStreamIsReset: a worker
// that RST-closes the socket BEFORE forwarding any event is a normal
// pre-acceptance transport error. The MeshRunner must be free to retry
// the task on another node, so RemoteRunner.Run must NOT swallow the raw
// read error as ErrNodeLost.
//
// The fix that turns every RST into ErrNodeLost is wrong: it would make
// the router never retry, contradicting the existing behaviour for
// connection-refused / no-route / DNS errors (also pre-acceptance). This
// test pins the distinction: a real event in the stream means "the node
// accepted the task" and only THEN is the loss worth NODE_LOST.
func TestRunReturnsRawPreAcceptanceErrorWhenTheStreamIsResetBeforeAnyEvent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hj, ok := w.(http.Hijacker)
		if !ok {
			t.Errorf("ResponseWriter is not a Hijacker")
			return
		}
		conn, _, err := hj.Hijack()
		if err != nil {
			return
		}
		// Lee y descarta el body del POST, pero NO envia nada por el
		// stream: cero eventos, cero status:running. La conexion se
		// cae antes de que el worker pudiera aceptar la tarea.
		go func() { _, _ = conn.Read(make([]byte, 4096)) }()
		time.Sleep(20 * time.Millisecond)
		// RST directo, sin haber enviado nada: el coordinator no
		// tiene forma de saber si la tarea llego a ejecutarse, asi
		// que el camino correcto es propagar el error para que el
		// MeshRunner reintente en otro nodo.
		if tcp, ok := conn.(*net.TCPConn); ok {
			_ = tcp.SetLinger(0)
		}
		_ = conn.Close()
	}))
	defer srv.Close()

	tr := &http.Transport{ResponseHeaderTimeout: 2 * time.Second}
	client := &http.Client{Transport: tr, Timeout: 5 * time.Second}

	sink := &capturingSink{}
	err := NewRemoteRunner(RemoteRunnerConfig{
		Endpoint: srv.URL,
		Client:   client,
	}).Run(context.Background(), protocol.TaskRequest{Agent: "worker", Task: "x"}, sink)

	// No debe ser ErrNodeLost: el worker no emitio nada, fallo
	// pre-aceptacion, el MeshRunner debe poder reintentar.
	if errors.Is(err, ErrNodeLost) {
		t.Fatalf("Run must NOT return ErrNodeLost for a pre-acceptance RST (no event was forwarded), got err=%v", err)
	}
	// El error debe ser no-nil y de tipo *net.OpError o io.ErrUnexpectedEOF,
	// exactamente el mismo tipo que el MeshRunner ya maneja como
	// isPreAcceptanceTransportError... aunque la implementacion actual
	// lo trata como terminal, no como reintentable. Lo importante es
	// que NO se mapee a ErrNodeLost: la politica de retry la decide
	// el caller.
	if err == nil {
		t.Fatalf("Run must return a non-nil error when the stream is reset before any event, got nil")
	}
	// Y el sink no debe tener eventos.
	if len(sink.events) != 0 {
		t.Fatalf("no events should be forwarded, got events=%v", sink.events)
	}
}
