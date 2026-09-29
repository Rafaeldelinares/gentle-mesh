package tlsutil_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAudit_NoInsecureSkipVerifyOutsideTLSUtil(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join(dir, "../.."))

	var violations []string
	err = filepath.Walk(repoRoot, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			if info != nil && info.IsDir() {
				base := filepath.Base(path)
				if base == ".git" || base == "vendor" || base == ".codegraph" {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(repoRoot, path)
		if strings.HasPrefix(rel, "pkg/tlsutil/") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.Contains(string(data), "InsecureSkipVerify") {
			violations = append(violations, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk failed: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("SECURITY VIOLATION: InsecureSkipVerify found outside pkg/tlsutil in: %s", strings.Join(violations, ", "))
	}
}
