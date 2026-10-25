// Hooks that expose the unexported workspace handlers to tests in other
// packages. They are compiled only during `go test`.
package http

import "net/http"

// HandleWorkspaceFileForTest invokes handleWorkspaceFile.
func (s *Server) HandleWorkspaceFileForTest(w http.ResponseWriter, r *http.Request) {
	s.handleWorkspaceFile(w, r)
}

// HandleWorkspaceTreeForTest invokes handleWorkspaceTree.
func (s *Server) HandleWorkspaceTreeForTest(w http.ResponseWriter, r *http.Request) {
	s.handleWorkspaceTree(w, r)
}
