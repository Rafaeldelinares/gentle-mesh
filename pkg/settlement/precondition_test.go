package settlement

import (
	"testing"
)

func TestValidatePreconditionCommand(t *testing.T) {
	tests := []struct {
		name      string
		cmd       string
		wantError bool
	}{
		// Permitted commands
		{name: "true without args", cmd: "true", wantError: false},
		{name: "false without args", cmd: "false", wantError: false},
		{name: "test file check", cmd: "test -f somefile.txt", wantError: false},
		{name: "test directory check", cmd: "test -d /tmp", wantError: false},
		{name: "bracket test check", cmd: "[ -f somefile.txt ]", wantError: false},
		{name: "go version", cmd: "go version", wantError: false},
		{name: "go env", cmd: "go env GOPATH", wantError: false},
		{name: "git status", cmd: "git status --porcelain", wantError: false},
		{name: "git rev-parse", cmd: "git rev-parse --is-inside-work-tree", wantError: false},
		{name: "git version", cmd: "git version", wantError: false},
		{name: "git diff --quiet", cmd: "git diff --quiet", wantError: false},
		{name: "python3 --version", cmd: "python3 --version", wantError: false},
		{name: "python -V", cmd: "python -V", wantError: false},
		{name: "node -v", cmd: "node -v", wantError: false},
		{name: "cargo --version", cmd: "cargo --version", wantError: false},
		{name: "rustc --version", cmd: "rustc --version", wantError: false},
		{name: "docker info", cmd: "docker info", wantError: false},
		{name: "docker version", cmd: "docker version", wantError: false},
		{name: "pytest --version", cmd: "pytest --version", wantError: false},
		{name: "trusted path /usr/bin/go version", cmd: "/usr/bin/go version", wantError: false},
		{name: "trusted path /bin/true", cmd: "/bin/true", wantError: false},

		// Rejected binaries (destructive or not in allowlist)
		{name: "touch marker", cmd: "touch /tmp/evil", wantError: true},
		{name: "rm file", cmd: "rm -rf /tmp/important", wantError: true},
		{name: "curl network", cmd: "curl https://attacker.com", wantError: true},
		{name: "sh subshell", cmd: "sh -c 'evil'", wantError: true},
		{name: "bash subshell", cmd: "bash -c 'evil'", wantError: true},
		{name: "untrusted path /home/user/bin/go", cmd: "/home/user/bin/go version", wantError: true},
		{name: "empty command", cmd: "", wantError: true},
		{name: "whitespace command", cmd: "   ", wantError: true},

		// Rejected arguments on allowed binaries (arbitrary code execution / mutation attempts)
		{name: "go run evil.go", cmd: "go run evil.go", wantError: true},
		{name: "go test", cmd: "go test ./...", wantError: true},
		{name: "go build", cmd: "go build main.go", wantError: true},
		{name: "git commit", cmd: "git commit -m 'evil'", wantError: true},
		{name: "git push", cmd: "git push origin main", wantError: true},
		{name: "git -c pager hook", cmd: "git -c core.pager=evil status", wantError: true},
		{name: "git diff without --quiet", cmd: "git diff", wantError: true},
		{name: "python3 -c code", cmd: "python3 -c 'import os; os.system(\"rm -rf /\")'", wantError: true},
		{name: "python -m module", cmd: "python -m http.server", wantError: true},
		{name: "python script.py", cmd: "python script.py", wantError: true},
		{name: "node -e code", cmd: "node -e 'process.exit(1)'", wantError: true},
		{name: "node script.js", cmd: "node script.js", wantError: true},
		{name: "cargo run", cmd: "cargo run", wantError: true},
		{name: "docker run", cmd: "docker run -it alpine", wantError: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parts, err := ValidatePreconditionCommand(tt.cmd)
			if tt.wantError && err == nil {
				t.Errorf("ValidatePreconditionCommand(%q) expected error, got parts: %v", tt.cmd, parts)
			}
			if !tt.wantError && err != nil {
				t.Errorf("ValidatePreconditionCommand(%q) unexpected error: %v", tt.cmd, err)
			}
		})
	}
}
