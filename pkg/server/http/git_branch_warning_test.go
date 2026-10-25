package http_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	meshhttp "github.com/gentleman-programming/gentle-mesh/pkg/server/http"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
)

// volatileTaskResponseFields are the per-request values of a task response; replacing them leaves
// the exact key set, key order and shape of the JSON body, so two responses of different requests
// can be compared byte by byte.
var volatileTaskResponseFields = []struct {
	re        *regexp.Regexp
	stableOut string
}{
	{regexp.MustCompile(`"task_id":"[^"]*"`), `"task_id":"ID"`},
	{regexp.MustCompile(`"status":"[^"]*"`), `"status":"STATUS"`},
	{regexp.MustCompile(`"events_url":"[^"]*"`), `"events_url":"URL"`},
	{regexp.MustCompile(`"created_at":\d+`), `"created_at":0`},
}

func normalizeTaskResponse(body string) string {
	out := body
	for _, f := range volatileTaskResponseFields {
		out = f.re.ReplaceAllString(out, f.stableOut)
	}
	return out
}

func postTaskBody(t *testing.T, ts *httptest.Server, body string) (int, string) {
	t.Helper()
	resp, err := ts.Client().Post(ts.URL+"/v1/tasks", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST /v1/tasks failed: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading the response body failed: %v", err)
	}
	return resp.StatusCode, strings.TrimSpace(string(raw))
}

func simulatedServer(t *testing.T, cfg func(*meshhttp.ServerConfig)) *httptest.Server {
	t.Helper()
	_, ts := setupTestServer(t, func(c *meshhttp.ServerConfig) {
		if c.Runner == nil {
			c.Runner = runner.NewSimulatedRunner(runner.SimulatedOptions{CompletionResult: "done"})
		}
		if cfg != nil {
			cfg(c)
		}
	})
	return ts
}

// TestServer_GitBranchWarningIsAdditive pins the additive contract: without git_branch the body is
// byte-identical to the pre-warning shape, and with git_branch the only difference is the new
// warnings field.
func TestServer_GitBranchWarningIsAdditive(t *testing.T) {
	ts := simulatedServer(t, nil)

	const sinRamaEsperada = `{"task_id":"ID","status":"STATUS","events_url":"URL","created_at":0}`

	status, body := postTaskBody(t, ts, `{"agent":"worker","task":"sin rama"}`)
	if status != http.StatusCreated {
		t.Fatalf("expected 201 without git_branch, got %d: %s", status, body)
	}
	if got := normalizeTaskResponse(body); got != sinRamaEsperada {
		t.Errorf("a response without git_branch must keep the exact pre-warning shape:\n got %s\nwant %s", got, sinRamaEsperada)
	}
	if strings.Contains(body, "warnings") {
		t.Errorf("a response without git_branch must not carry warnings: %s", body)
	}

	conRamaEsperada := `{"task_id":"ID","status":"STATUS","events_url":"URL","created_at":0,"warnings":["` + protocol.GitBranchLockOnlyWarning + `"]}`

	// A branch that does not exist is still accepted: git_branch is a lock key, not a checkout.
	status, body = postTaskBody(t, ts, `{"agent":"worker","task":"con rama","git_repo":"acme/mesh","git_branch":"rama-que-no-existe-xyz"}`)
	if status != http.StatusCreated {
		t.Fatalf("a non-existent git_branch must still be accepted with 201, got %d: %s", status, body)
	}
	if got := normalizeTaskResponse(body); got != conRamaEsperada {
		t.Errorf("with git_branch the only difference must be the warnings field:\n got %s\nwant %s", got, conRamaEsperada)
	}
}

// TestServer_GitBranchWarningOnIdempotentReplay covers the other success path: an idempotent
// replay answers 200 and carries the same additive warning.
func TestServer_GitBranchWarningOnIdempotentReplay(t *testing.T) {
	ts := simulatedServer(t, nil)
	const req = `{"agent":"worker","task":"replay","idempotency_key":"key-warnings","git_repo":"acme/mesh","git_branch":"feature/x"}`

	status, body := postTaskBody(t, ts, req)
	if status != http.StatusCreated {
		t.Fatalf("expected 201 on the first dispatch, got %d: %s", status, body)
	}
	if !strings.Contains(body, protocol.GitBranchLockOnlyWarning) {
		t.Errorf("the 201 must carry the warning, got %s", body)
	}

	status, body = postTaskBody(t, ts, req)
	if status != http.StatusOK {
		t.Fatalf("expected 200 for the idempotent replay, got %d: %s", status, body)
	}
	if !strings.Contains(body, protocol.GitBranchLockOnlyWarning) {
		t.Errorf("the idempotent 200 must carry the same additive warning, got %s", body)
	}
}

// TestServer_GitBranchWarningKeepsLockExclusive checks that the advisory is additive only: the
// branch lock keeps rejecting a second task on the same repo+branch and the conflict body does not
// gain the new field.
func TestServer_GitBranchWarningKeepsLockExclusive(t *testing.T) {
	ts := simulatedServer(t, func(c *meshhttp.ServerConfig) {
		c.TerritoryMode = protocol.TerritoryModeStrict
		c.Runner = runner.NewSimulatedRunner(runner.SimulatedOptions{
			Query: &runner.SimulatedQuery{QueryID: "holding", Question: "holding the lock"},
		})
	})

	status, body := postTaskBody(t, ts, `{"agent":"a","task":"uno","git_repo":"acme/mesh","git_branch":"feature/lock"}`)
	if status != http.StatusCreated {
		t.Fatalf("the first task must take the branch, got %d: %s", status, body)
	}
	if !strings.Contains(body, protocol.GitBranchLockOnlyWarning) {
		t.Errorf("the first task must carry the warning, got %s", body)
	}

	status, body = postTaskBody(t, ts, `{"agent":"b","task":"dos","git_repo":"acme/mesh","git_branch":"feature/lock"}`)
	if status != http.StatusConflict {
		t.Fatalf("the lock must stay exclusive, expected 409, got %d: %s", status, body)
	}
	if !strings.Contains(body, "branch is locked") {
		t.Errorf("the conflict message must be unchanged, got %s", body)
	}
	if strings.Contains(body, "warnings") {
		t.Errorf("the conflict body must not change, got %s", body)
	}
}
