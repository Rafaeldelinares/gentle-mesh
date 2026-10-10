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
