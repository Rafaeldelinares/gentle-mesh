package store_test

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAudit_NoKeyPEMInServerStore enforces that server and protocol packages never
// define, read, write, or transport private key fields or columns (key_pem or KeyPEM).
func TestAudit_NoKeyPEMInServerStore(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get working dir: %v", err)
	}

	repoRoot := filepath.Clean(filepath.Join(dir, "../../.."))

	searchDirs := []string{
		filepath.Join(repoRoot, "pkg", "server"),
		filepath.Join(repoRoot, "pkg", "protocol"),
	}

	for _, searchDir := range searchDirs {
		err := filepath.Walk(searchDir, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() {
				return nil
			}
			// Only audit non-test Go source files
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}

			relPath, _ := filepath.Rel(repoRoot, path)
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			defer file.Close()

			scanner := bufio.NewScanner(file)
			lineNum := 0
			for scanner.Scan() {
				lineNum++
				line := scanner.Text()

				// KeyPEM is strictly forbidden everywhere
				if strings.Contains(line, "KeyPEM") {
					t.Errorf("security violation in %s:%d: forbidden token 'KeyPEM' found; private keys must never exist on server structs", relPath, lineNum)
				}

				// key_pem is forbidden in SELECT, INSERT, CREATE TABLE, or struct definitions
				if strings.Contains(line, "key_pem") {
					// The only allowed occurrence is the schema cleanup migration dropping the legacy column
					isAllowedMigration := relPath == "pkg/server/store/certstore.go" &&
						(strings.Contains(line, "DROP COLUMN key_pem") ||
							strings.Contains(line, `name == "key_pem"`) ||
							strings.Contains(line, "UPDATE node_certs SET key_pem = NULL") ||
							strings.Contains(line, "// Migration:"))

					if !isAllowedMigration {
						t.Errorf("security violation in %s:%d: forbidden token 'key_pem' found in active server code: %s", relPath, lineNum, strings.TrimSpace(line))
					}
				}
			}
			return scanner.Err()
		})
		if err != nil {
			t.Fatalf("failed to walk directory %s: %v", searchDir, err)
		}
	}
}
