//go:build !testharness

package agent

import "net/http"

// registerTestHarnessEndpoints is a no-op stub for non-testharness builds.
// When building with -tags testharness, the real implementation is provided
// by server_harness.go and this file is excluded.
func (s *Server) registerTestHarnessEndpoints(mux *http.ServeMux) {
	// no-op: /execute and /inject-receipt are not registered in production builds
}
