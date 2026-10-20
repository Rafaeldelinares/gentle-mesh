package http

import (
	"encoding/json"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"
)

// The POST /v1/certs/enroll bootstrap route must reach its handler without a
// bearer token: the enrollment token in the body is the only credential a node
// has before it owns a certificate, and MTLSMiddleware already exempts it.
// The exemption is limited to that exact method and path.
func TestAuthMiddlewareEnrollBootstrapExemption(t *testing.T) {
	const bearer = "demo123"

	reached := stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		w.WriteHeader(stdhttp.StatusOK)
	})
	handler := AuthMiddleware(bearer, reached)

	tests := []struct {
		name       string
		method     string
		path       string
		authHeader string
		wantStatus int
	}{
		{"POST /v1/certs/enroll without Authorization is exempt", stdhttp.MethodPost, "/v1/certs/enroll", "", stdhttp.StatusOK},
		{"POST /v1/certs/enroll ignores a wrong bearer", stdhttp.MethodPost, "/v1/certs/enroll", "Bearer wrong-token", stdhttp.StatusOK},
		{"GET /v1/certs/enroll still requires the bearer", stdhttp.MethodGet, "/v1/certs/enroll", "", stdhttp.StatusUnauthorized},
		{"POST /v1/certs/enroll/x still requires the bearer", stdhttp.MethodPost, "/v1/certs/enroll/x", "", stdhttp.StatusUnauthorized},
		{"POST /v1/certs/revoke still requires the bearer", stdhttp.MethodPost, "/v1/certs/revoke", "", stdhttp.StatusUnauthorized},
		{"GET /v1/mesh/nodes still requires the bearer", stdhttp.MethodGet, "/v1/mesh/nodes", "", stdhttp.StatusUnauthorized},
		{"GET /v1/mesh/nodes passes with the bearer", stdhttp.MethodGet, "/v1/mesh/nodes", "Bearer " + bearer, stdhttp.StatusOK},
		{"GET /healthz is still exempt", stdhttp.MethodGet, "/healthz", "", stdhttp.StatusOK},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("%s %s: got status %d, want %d (body %q)",
					tc.method, tc.path, rec.Code, tc.wantStatus, rec.Body.String())
			}

			if tc.wantStatus == stdhttp.StatusUnauthorized {
				var body map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("401 body is not JSON: %v (body %q)", err, rec.Body.String())
				}
				if body["error"] != "unauthorized" {
					t.Fatalf("401 body changed: %v", body)
				}
			}
		})
	}
}

// Without a configured token the middleware keeps being a no-op.
func TestAuthMiddlewareEmptyTokenIsNoOp(t *testing.T) {
	reached := stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		w.WriteHeader(stdhttp.StatusOK)
	})
	handler := AuthMiddleware("", reached)

	req := httptest.NewRequest(stdhttp.MethodGet, "/v1/mesh/nodes", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != stdhttp.StatusOK {
		t.Fatalf("empty token must be a no-op, got status %d", rec.Code)
	}
}

// TestAuthMiddlewareWithNodeTokenScoping validates that node-token is strictly
// scoped to node join and heartbeat (POST /v1/mesh/join, POST /v1/mesh/heartbeat).
// Any other route with node-token returns 403 Forbidden.
// The admin token remains valid across all routes.
func TestAuthMiddlewareWithNodeTokenScoping(t *testing.T) {
	const adminToken = "admin-secret"
	const nodeToken = "node-secret"

	reached := stdhttp.HandlerFunc(func(w stdhttp.ResponseWriter, _ *stdhttp.Request) {
		w.WriteHeader(stdhttp.StatusOK)
	})

	handler := AuthMiddlewareWithConfig(AuthConfig{
		AdminToken: adminToken,
		NodeToken:  nodeToken,
	}, reached)

	tests := []struct {
		name       string
		method     string
		path       string
		token      string
		wantStatus int
		wantError  string
	}{
		// Admin token passes everywhere
		{"Admin token passes POST /v1/mesh/join", stdhttp.MethodPost, "/v1/mesh/join", adminToken, stdhttp.StatusOK, ""},
		{"Admin token passes POST /v1/mesh/heartbeat", stdhttp.MethodPost, "/v1/mesh/heartbeat", adminToken, stdhttp.StatusOK, ""},
		{"Admin token passes POST /v1/tasks", stdhttp.MethodPost, "/v1/tasks", adminToken, stdhttp.StatusOK, ""},
		{"Admin token passes GET /v1/mesh/nodes", stdhttp.MethodGet, "/v1/mesh/nodes", adminToken, stdhttp.StatusOK, ""},

		// Node token only passes POST /v1/mesh/join and POST /v1/mesh/heartbeat
		{"Node token passes POST /v1/mesh/join", stdhttp.MethodPost, "/v1/mesh/join", nodeToken, stdhttp.StatusOK, ""},
		{"Node token passes POST /v1/mesh/heartbeat", stdhttp.MethodPost, "/v1/mesh/heartbeat", nodeToken, stdhttp.StatusOK, ""},
		{"Node token returns 403 on POST /v1/tasks", stdhttp.MethodPost, "/v1/tasks", nodeToken, stdhttp.StatusForbidden, "forbidden"},
		{"Node token returns 403 on GET /v1/mesh/nodes", stdhttp.MethodGet, "/v1/mesh/nodes", nodeToken, stdhttp.StatusForbidden, "forbidden"},
		{"Node token returns 403 on GET /v1/mesh/join", stdhttp.MethodGet, "/v1/mesh/join", nodeToken, stdhttp.StatusForbidden, "forbidden"},
		{"Node token returns 403 on DELETE /v1/mesh/peers/1", stdhttp.MethodDelete, "/v1/mesh/peers/1", nodeToken, stdhttp.StatusForbidden, "forbidden"},

		// Wrong or missing token returns 401 Unauthorized
		{"Wrong token on POST /v1/mesh/join returns 401", stdhttp.MethodPost, "/v1/mesh/join", "bad-token", stdhttp.StatusUnauthorized, "unauthorized"},
		{"Wrong token on POST /v1/tasks returns 401", stdhttp.MethodPost, "/v1/tasks", "bad-token", stdhttp.StatusUnauthorized, "unauthorized"},
		{"Missing token on POST /v1/mesh/join returns 401", stdhttp.MethodPost, "/v1/mesh/join", "", stdhttp.StatusUnauthorized, "unauthorized"},
		{"Missing token on POST /v1/tasks returns 401", stdhttp.MethodPost, "/v1/tasks", "", stdhttp.StatusUnauthorized, "unauthorized"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("%s %s: got status %d, want %d (body %q)",
					tc.method, tc.path, rec.Code, tc.wantStatus, rec.Body.String())
			}

			if tc.wantError != "" {
				var body map[string]string
				if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
					t.Fatalf("error body is not JSON: %v (body %q)", err, rec.Body.String())
				}
				if body["error"] != tc.wantError {
					t.Fatalf("error mismatch: got %q, want %q", body["error"], tc.wantError)
				}
			}
		})
	}
}
