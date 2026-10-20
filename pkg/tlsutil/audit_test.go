package tlsutil_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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

// TestAudit_NoLiteralTrueInDevInsecure verifies that no production Go code calls
// DevInsecureConfig, ApplyDevInsecure, or WithDevInsecureTLS with literal 'true'.
func TestAudit_NoLiteralTrueInDevInsecure(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join(dir, "../.."))

	pattern := regexp.MustCompile(`(ApplyDevInsecure|DevInsecureConfig|DevInsecureConfigWithOutput|WithDevInsecureTLS)\([^)]*\btrue\b`)

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
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		content := string(data)
		// Exempt files with testharness or redteam build tags
		if strings.Contains(content, "testharness") || strings.Contains(content, "redteam") {
			return nil
		}
		if pattern.MatchString(content) {
			rel, _ := filepath.Rel(repoRoot, path)
			violations = append(violations, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk failed: %v", err)
	}
	if len(violations) > 0 {
		t.Fatalf("SECURITY VIOLATION: literal 'true' passed to dev-insecure call outside test/harness in: %s", strings.Join(violations, ", "))
	}
}

// TestAudit_SecurityGates_CatchesWithDevInsecureTLSTrue verifies that the security-gates.sh
// script actively rejects WithDevInsecureTLS(true) outside tests and passes cleanly when absent.
func TestAudit_SecurityGates_CatchesWithDevInsecureTLSTrue(t *testing.T) {
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoRoot := filepath.Clean(filepath.Join(dir, "../.."))
	gateScript := filepath.Join(repoRoot, "scripts/security-gates.sh")
	if _, err := os.Stat(gateScript); err != nil {
		t.Skip("security-gates.sh not found")
	}

	// 1. Self-tests of the security gate script must pass
	cmdSelfTest := exec.Command(gateScript, "--test")
	cmdSelfTest.Dir = repoRoot
	outSelf, errSelf := cmdSelfTest.CombinedOutput()
	if errSelf != nil {
		t.Fatalf("security-gates.sh --test failed: %v\noutput: %s", errSelf, string(outSelf))
	}

	// 2. Behavioral verification: temporary production file with WithDevInsecureTLS(true) must fail gate
	tempProdFile := filepath.Join(repoRoot, "pkg/shell/gate_test_temp_violation.go")
	badContent := "package shell\n\nimport \"github.com/Rafaeldelinares/gentle-mesh/integration/agent\"\n\nfunc BadGate() { _ = agent.WithDevInsecureTLS(true) }\n"
	if err := os.WriteFile(tempProdFile, []byte(badContent), 0644); err != nil {
		t.Fatalf("write temp prod violation: %v", err)
	}
	defer os.Remove(tempProdFile)

	cmdFail := exec.Command(gateScript)
	cmdFail.Dir = repoRoot
	outFail, errFail := cmdFail.CombinedOutput()
	if errFail == nil {
		t.Fatalf("security-gates.sh should have failed on WithDevInsecureTLS(true) in prod file, but passed!\noutput: %s", string(outFail))
	}
	if !strings.Contains(string(outFail), "gate_test_temp_violation.go") || !strings.Contains(string(outFail), "WithDevInsecureTLS") {
		t.Fatalf("security-gates.sh did not report expected violation:\noutput: %s", string(outFail))
	}

	// 3. Clean up and verify clean gate pass
	_ = os.Remove(tempProdFile)
	cmdClean := exec.Command(gateScript)
	cmdClean.Dir = repoRoot
	outClean, errClean := cmdClean.CombinedOutput()
	if errClean != nil {
		t.Fatalf("security-gates.sh should pass cleanly after removing violation, got error: %v\noutput: %s", errClean, string(outClean))
	}
}
