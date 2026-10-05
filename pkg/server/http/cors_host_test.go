package http_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	meshhttp "github.com/gentleman-programming/gentle-mesh/pkg/server/http"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
)

func TestServer_CORSRejectsUnknownOrigin(t *testing.T) {
	_, ts := setupTestServer(t)

	// GET with unknown origin -> no Access-Control-Allow-Origin header
	getReq, err := http.NewRequest(http.MethodGet, ts.URL+"/healthz", nil)
	if err != nil {
		t.Fatalf("failed building GET request: %v", err)
	}
	getReq.Header.Set("Origin", "https://evil.example")
	getResp, err := ts.Client().Do(getReq)
	if err != nil {
		t.Fatalf("GET /healthz failed: %v", err)
	}
	defer getResp.Body.Close()

	if got := getResp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("expected no Access-Control-Allow-Origin for unknown origin, got %q", got)
	}

	// OPTIONS (preflight) with unknown origin -> 403 with {"error":"origin not allowed"}
	optReq, err := http.NewRequest(http.MethodOptions, ts.URL+"/v1/tasks", nil)
	if err != nil {
		t.Fatalf("failed building OPTIONS request: %v", err)
	}
	optReq.Header.Set("Origin", "https://evil.example")
	optReq.Header.Set("Access-Control-Request-Method", http.MethodPost)
	optResp, err := ts.Client().Do(optReq)
	if err != nil {
		t.Fatalf("OPTIONS /v1/tasks failed: %v", err)
	}
	defer optResp.Body.Close()

	if optResp.StatusCode != http.StatusForbidden {
		t.Errorf("expected status 403 Forbidden for unknown origin preflight, got %d", optResp.StatusCode)
	}
	if got := optResp.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("expected no Access-Control-Allow-Origin on rejected preflight, got %q", got)
	}
	var optErrResp map[string]string
	if err := json.NewDecoder(optResp.Body).Decode(&optErrResp); err != nil {
		t.Errorf("failed decoding preflight rejection body: %v", err)
	} else if optErrResp["error"] != "origin not allowed" {
		t.Errorf("expected error %q, got %q", "origin not allowed", optErrResp["error"])
	}
}

func TestServer_CORSAllowsDefaultOrigins(t *testing.T) {
	_, ts := setupTestServer(t)

	origins := []string{
		"tauri://localhost",
		"http://localhost:5173",
		"http://127.0.0.1:3000",
		"http://100.64.0.1:8080",
		"https://node.ts.net",
	}

	for _, origin := range origins {
		t.Run(origin, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, ts.URL+"/healthz", nil)
			if err != nil {
				t.Fatalf("failed building GET request: %v", err)
			}
			req.Header.Set("Origin", origin)
			resp, err := ts.Client().Do(req)
			if err != nil {
				t.Fatalf("GET /healthz failed: %v", err)
			}
			defer resp.Body.Close()

			got := resp.Header.Get("Access-Control-Allow-Origin")
			if got == "*" {
				t.Errorf("Access-Control-Allow-Origin must never be '*' when Origin is present, got '*'")
			}
			if got != origin {
				t.Errorf("expected Access-Control-Allow-Origin %q, got %q", origin, got)
			}
			if !strings.Contains(strings.ToLower(resp.Header.Get("Vary")), "origin") {
				t.Errorf("expected Vary header to contain Origin, got %q", resp.Header.Get("Vary"))
			}
		})
	}
}

func TestServer_CORSNeverWildcardWithOrigin(t *testing.T) {
	_, ts := setupTestServer(t)

	const origin = "http://localhost:5173"
	req, err := http.NewRequest(http.MethodGet, ts.URL+"/healthz", nil)
	if err != nil {
		t.Fatalf("failed building GET request: %v", err)
	}
	req.Header.Set("Origin", origin)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /healthz failed: %v", err)
	}
	defer resp.Body.Close()

	got := resp.Header.Get("Access-Control-Allow-Origin")
	if got == "*" {
		t.Fatalf("expected Access-Control-Allow-Origin != '*', got '*'")
	}
	if got != origin {
		t.Errorf("expected Access-Control-Allow-Origin %q, got %q", origin, got)
	}
}

func TestServer_CORSAdditionalOrigins(t *testing.T) {
	_, ts := setupTestServer(t, func(cfg *meshhttp.ServerConfig) {
		cfg.CORSOrigins = []string{"https://app.example.com"}
	})

	// Configured extra origin is echoed
	reqAllowed, err := http.NewRequest(http.MethodGet, ts.URL+"/healthz", nil)
	if err != nil {
		t.Fatalf("failed building request: %v", err)
	}
	reqAllowed.Header.Set("Origin", "https://app.example.com")
	respAllowed, err := ts.Client().Do(reqAllowed)
	if err != nil {
		t.Fatalf("GET /healthz failed: %v", err)
	}
	defer respAllowed.Body.Close()

	if got := respAllowed.Header.Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Errorf("expected Access-Control-Allow-Origin %q, got %q", "https://app.example.com", got)
	}

	// Other origin is not echoed
	reqOther, err := http.NewRequest(http.MethodGet, ts.URL+"/healthz", nil)
	if err != nil {
		t.Fatalf("failed building request: %v", err)
	}
	reqOther.Header.Set("Origin", "https://other.example.com")
	respOther, err := ts.Client().Do(reqOther)
	if err != nil {
		t.Fatalf("GET /healthz failed: %v", err)
	}
	defer respOther.Body.Close()

	if got := respOther.Header.Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("expected no Access-Control-Allow-Origin for %q, got %q", "https://other.example.com", got)
	}
}

func TestServer_HostValidation(t *testing.T) {
	_, ts := setupTestServer(t, func(cfg *meshhttp.ServerConfig) {
		cfg.AllowedHosts = []string{"mycoord.example"}
	})

	// Host "evil.example" -> 421 Misdirected Request
	reqEvil, err := http.NewRequest(http.MethodGet, ts.URL+"/healthz", nil)
	if err != nil {
		t.Fatalf("failed building request: %v", err)
	}
	reqEvil.Host = "evil.example"
	respEvil, err := ts.Client().Do(reqEvil)
	if err != nil {
		t.Fatalf("request with Host 'evil.example' failed: %v", err)
	}
	defer respEvil.Body.Close()

	if respEvil.StatusCode != http.StatusMisdirectedRequest {
		t.Errorf("expected 421 Misdirected Request for Host 'evil.example', got %d", respEvil.StatusCode)
	}
	var evilErrResp map[string]string
	if err := json.NewDecoder(respEvil.Body).Decode(&evilErrResp); err != nil {
		t.Errorf("failed decoding host rejection body: %v", err)
	} else if evilErrResp["error"] != "invalid host" {
		t.Errorf("expected error %q, got %q", "invalid host", evilErrResp["error"])
	}

	allowedHosts := []string{
		"localhost:1234",
		"LOCALHOST",
		"127.0.0.1:8080",
		"[::1]:8080",
		"node.ts.net",
		"100.64.0.1",
		"mycoord.example",
	}

	for _, h := range allowedHosts {
		t.Run("allowed_"+h, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, ts.URL+"/healthz", nil)
			if err != nil {
				t.Fatalf("failed building request: %v", err)
			}
			req.Host = h
			resp, err := ts.Client().Do(req)
			if err != nil {
				t.Fatalf("request with Host %q failed: %v", h, err)
			}
			defer resp.Body.Close()

			if resp.StatusCode == http.StatusMisdirectedRequest {
				t.Errorf("Host %q should be allowed, got 421 Misdirected Request", h)
			}
		})
	}
}

func TestServer_HostValidationDerivesInterfaceAddresses(t *testing.T) {
	var localIP string
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		t.Skipf("cannot enumerate interface addresses: %v", err)
	}
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok {
			continue
		}
		ip := ipNet.IP
		if !ip.IsLoopback() && ip.To4() != nil {
			localIP = ip.String()
			break
		}
	}
	if localIP == "" {
		t.Skip("no non-loopback local IPv4 interface found")
	}

	tasksDir := t.TempDir()
	cfg := meshhttp.ServerConfig{
		Addr:             "0.0.0.0:0",
		TasksDir:         tasksDir,
		HeartbeatTimeout: 5 * time.Second,
		TaskTTL:          time.Hour,
		WorkspaceRoot:    tasksDir,
		DBPath:           "none",
	}
	srv, err := meshhttp.NewServer(cfg)
	if err != nil {
		t.Fatalf("failed creating server: %v", err)
	}
	defer srv.Shutdown(context.Background())

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	req, err := http.NewRequest(http.MethodGet, ts.URL+"/healthz", nil)
	if err != nil {
		t.Fatalf("failed building request: %v", err)
	}
	req.Host = localIP
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /healthz with Host %q failed: %v", localIP, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusMisdirectedRequest {
		t.Errorf("expected Host %q derived from interface addrs to not be 421, got 421", localIP)
	}
}

func TestServer_RequirePiRunnerRejectsNonLoopbackWithoutAuth(t *testing.T) {
	tasksDir := t.TempDir()
	workspaceRoot := t.TempDir()
	piRunner := runner.NewPiRunner(runner.PiRunnerOptions{
		WorkspaceRoot: workspaceRoot,
	})

	cfg := meshhttp.ServerConfig{
		Addr:             "0.0.0.0:0",
		TasksDir:         tasksDir,
		HeartbeatTimeout: 5 * time.Second,
		TaskTTL:          time.Hour,
		WorkspaceRoot:    workspaceRoot,
		DBPath:           "none",
		Runner:           piRunner,
		BearerToken:      "",
		RequireMTLS:      false,
	}

	srv, err := meshhttp.NewServer(cfg)
	if err != nil {
		t.Fatalf("failed creating server: %v", err)
	}
	defer srv.Shutdown(context.Background())

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start()
	}()

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected Start() to return a non-nil error for PiRunner on non-loopback address without auth, got nil")
		}
	case <-time.After(500 * time.Millisecond):
		_ = srv.Shutdown(context.Background())
		t.Fatal("expected Start() to reject synchronously before listening, but it blocked or started listening")
	}
}

func TestServer_PiRunnerAllowedWithInsecureNoAuth(t *testing.T) {
	tasksDir := t.TempDir()
	workspaceRoot := t.TempDir()
	piRunner := runner.NewPiRunner(runner.PiRunnerOptions{
		WorkspaceRoot: workspaceRoot,
	})

	cfg := meshhttp.ServerConfig{
		Addr:             "127.0.0.1:0",
		TasksDir:         tasksDir,
		HeartbeatTimeout: 5 * time.Second,
		TaskTTL:          time.Hour,
		WorkspaceRoot:    workspaceRoot,
		DBPath:           "none",
		Runner:           piRunner,
		InsecureNoAuth:   true,
	}

	srv, err := meshhttp.NewServer(cfg)
	if err != nil {
		t.Fatalf("failed creating server: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start()
	}()

	// Brief wait to ensure Start() has commenced listening
	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("failed to shutdown server: %v", err)
	}

	startErr := <-errCh
	if startErr != nil && !errors.Is(startErr, http.ErrServerClosed) {
		t.Errorf("Start() returned unexpected error when InsecureNoAuth is true: %v", startErr)
	}
}

func TestServer_PiRunnerAllowedWithToken(t *testing.T) {
	tasksDir := t.TempDir()
	workspaceRoot := t.TempDir()
	piRunner := runner.NewPiRunner(runner.PiRunnerOptions{
		WorkspaceRoot: workspaceRoot,
	})

	cfg := meshhttp.ServerConfig{
		Addr:             "0.0.0.0:0",
		TasksDir:         tasksDir,
		HeartbeatTimeout: 5 * time.Second,
		TaskTTL:          time.Hour,
		WorkspaceRoot:    workspaceRoot,
		DBPath:           "none",
		Runner:           piRunner,
		BearerToken:      "mesh-secret-token",
	}

	srv, err := meshhttp.NewServer(cfg)
	if err != nil {
		t.Fatalf("failed creating server: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start()
	}()

	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("failed to shutdown server: %v", err)
	}

	startErr := <-errCh
	if startErr != nil && !errors.Is(startErr, http.ErrServerClosed) {
		t.Errorf("Start() returned unexpected error when a bearer token is configured: %v", startErr)
	}
}

func TestServer_NonPiRunnerWarnsOnNonLoopbackWithoutAuth(t *testing.T) {
	var logBuf bytes.Buffer
	prevWriter := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevWriter)

	tasksDir := t.TempDir()
	cfg := meshhttp.ServerConfig{
		Addr:             "0.0.0.0:0",
		TasksDir:         tasksDir,
		HeartbeatTimeout: 5 * time.Second,
		TaskTTL:          time.Hour,
		WorkspaceRoot:    tasksDir,
		DBPath:           "none",
		// Runner is nil, so the server selects the mesh/simulated runner.
	}

	srv, err := meshhttp.NewServer(cfg)
	if err != nil {
		t.Fatalf("failed creating server: %v", err)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- srv.Start()
	}()

	time.Sleep(50 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		t.Fatalf("failed to shutdown server: %v", err)
	}

	startErr := <-errCh
	if startErr != nil && !errors.Is(startErr, http.ErrServerClosed) {
		t.Errorf("Start() returned unexpected error for a non-pi runner: %v", startErr)
	}
	if !strings.Contains(logBuf.String(), "WARNING") {
		t.Errorf("expected a non-loopback warning to be logged, got %q", logBuf.String())
	}
}
