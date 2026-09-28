//go:build testharness

package agent

import "net/http"

// registerTestHarnessEndpoints registers /execute and /inject-receipt.
// These endpoints bypass security checks and are ONLY for test scenarios.
func (s *Server) registerTestHarnessEndpoints(mux *http.ServeMux) {
	mux.HandleFunc("POST /execute", s.handleExecute)
	mux.HandleFunc("POST /inject-receipt", s.handleInjectReceipt)
}
