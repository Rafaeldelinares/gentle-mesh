package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestThreatModel_AllRegisteredRoutesCovered verifies that every HTTP route
// registered in integration/agent (server.go, server_harness.go) and in
// pkg/server/http (handlers.go) is explicitly documented in docs/architecture/THREAT-MODEL.md.
//
// This guarantees that the threat model never falls out of sync or documents
// fictitious routes.
func TestThreatModel_AllRegisteredRoutesCovered(t *testing.T) {
	repoRoot := findRepoRoot(t)
	threatModelPath := filepath.Join(repoRoot, "docs", "architecture", "THREAT-MODEL.md")
	contentBytes, err := os.ReadFile(threatModelPath)
	if err != nil {
		t.Fatalf("failed to read THREAT-MODEL.md: %v", err)
	}
	threatModelContent := string(contentBytes)

	filesToScan := []string{
		filepath.Join(repoRoot, "integration", "agent", "server.go"),
		filepath.Join(repoRoot, "integration", "agent", "server_harness.go"),
		filepath.Join(repoRoot, "pkg", "server", "http", "handlers.go"),
	}

	routesFound := 0
	for _, file := range filesToScan {
		relFile, _ := filepath.Rel(repoRoot, file)
		routes := extractRoutesFromSource(t, file)
		if len(routes) == 0 {
			t.Errorf("no routes extracted from %s", relFile)
			continue
		}

		for _, r := range routes {
			routesFound++
			// Clean pattern to path (e.g., "POST /v1/tasks" -> "/v1/tasks", "/health" -> "/health")
			path := cleanRoutePath(r.Pattern)

			if !strings.Contains(threatModelContent, path) {
				t.Errorf("[%s] Route %q (path: %q) is registered in code (%s) but MISSING from THREAT-MODEL.md",
					relFile, r.Pattern, path, r.HandlerFunc)
			}

			// Verify handler function citation
			if r.HandlerFunc != "" && !strings.Contains(threatModelContent, r.HandlerFunc) {
				t.Errorf("[%s] Handler function %q for route %q is registered in code but MISSING from THREAT-MODEL.md citations",
					relFile, r.HandlerFunc, r.Pattern)
			}
		}
	}

	t.Logf("Verified %d registered routes and handlers are covered in THREAT-MODEL.md", routesFound)
}

type registeredRoute struct {
	Pattern     string
	HandlerFunc string
}

func extractRoutesFromSource(t *testing.T, filePath string) []registeredRoute {
	fset := token.NewFileSet()
	node, err := parser.ParseFile(fset, filePath, nil, 0)
	if err != nil {
		t.Fatalf("parse file %s: %v", filePath, err)
	}

	var routes []registeredRoute
	ast.Inspect(node, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}

		// Look for mux.HandleFunc(pattern, handler)
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "HandleFunc" {
			return true
		}

		if len(call.Args) >= 2 {
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			pattern := strings.Trim(lit.Value, `"`)

			handlerName := ""
			switch h := call.Args[1].(type) {
			case *ast.SelectorExpr:
				handlerName = h.Sel.Name
			case *ast.Ident:
				handlerName = h.Name
			}

			routes = append(routes, registeredRoute{
				Pattern:     pattern,
				HandlerFunc: handlerName,
			})
		}
		return true
	})

	return routes
}

func cleanRoutePath(pattern string) string {
	parts := strings.Fields(pattern)
	if len(parts) == 2 {
		return parts[1]
	}
	return pattern
}

func findRepoRoot(t *testing.T) string {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (go.mod not found)")
		}
		dir = parent
	}
}
