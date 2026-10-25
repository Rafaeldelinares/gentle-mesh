package runner_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gentleman-programming/gentle-mesh/pkg/protocol"
	"github.com/gentleman-programming/gentle-mesh/pkg/server/runner"
)

// The runner enforces the completion contract deterministically: an agent that
// reports "done" without a commit fails the task with NO_COMMIT_PRODUCED. Two
// things must therefore be true inside the sandbox, and neither was true when
// this pair of tests was written:
//
//  1. The agent is told that committing is part of the contract. The runner
//     directive only carried the tool-call budget, so a model that wrote its
//     files and stopped produced an empty proposal.
//  2. The agent can actually commit. The sandbox mounts /etc read-only and
//     hides the user's HOME under a tmpfs, and neither host has /etc/gitconfig,
//     so `git commit` inside the sandbox failed with "Author identity unknown".

// runAndCaptureSandboxArgs runs one happy-path task through the fake
// bwrap/gentle-shell pair and returns the bwrap argv and the prompt the agent
// was given.
func runAndCaptureSandboxArgs(t *testing.T) (bwrapArgs []string, prompt string) {
	t.Helper()

	cache, worktreesRoot := seedBareCache(t)

	bwrapArgsPath := filepath.Join(t.TempDir(), "bwrap-argv.txt")
	gsArgsPath := filepath.Join(t.TempDir(), "gs-argv.txt")

	bwrapBody := "printf '%s\\n' \"$@\" > " + shellQuote(bwrapArgsPath) + "\n" +
		"for arg in \"$@\"; do echo \"$arg\" >> " + shellQuote(gsArgsPath) + "\ndone\n" +
		passBwrapScript
	bwrapScript := writeFakeBwrap(t, bwrapBody)
	gsScript := writeFakeGentleShell(t, fakeGentleShellScript(gsArgsPath, gentleShellHappyStream, "", 0))

	r := runner.NewGentleShellRunner(defaultRunnerOptions(t, runner.GentleShellRunnerOptions{
		Bwrap:         bwrapScript,
		GentleShell:   gsScript,
		RepoCache:     cache,
		WorktreesRoot: worktreesRoot,
		ThinkingLevel: "minimal",
		TotalTimeout:  2 * time.Minute,
		IdleTimeout:   30 * time.Second,
	}))

	sink := newMockSink(context.Background())
	if err := r.Run(context.Background(), protocol.TaskRequest{
		TaskID: "task-contract",
		Agent:  "worker",
		Task:   "create PRUEBA.md",
	}, sink); err != nil {
		t.Fatalf("expected nil error, got: %v", err)
	}

	return readLines(t, bwrapArgsPath), strings.Join(readLines(t, gsArgsPath), "\n")
}

func TestGentleShellRunner_PromptStatesTheCompletionContract(t *testing.T) {
	_, prompt := runAndCaptureSandboxArgs(t)

	if !strings.Contains(prompt, "git commit") {
		t.Errorf("the runner directive must tell the agent to commit its work with `git commit`, got prompt: %q", prompt)
	}
	if !strings.Contains(prompt, "NO_COMMIT_PRODUCED") {
		t.Errorf("the runner directive must name the failure the agent avoids (NO_COMMIT_PRODUCED), got prompt: %q", prompt)
	}
	// The budget directive the runner already appended must survive.
	if !strings.Contains(prompt, "[runner directive] task_id=task-contract") {
		t.Errorf("expected the existing runner directive to be preserved, got prompt: %q", prompt)
	}
}

func TestGentleShellRunner_SandboxProvidesAGitIdentity(t *testing.T) {
	bwrapArgs, _ := runAndCaptureSandboxArgs(t)

	for _, want := range []string{
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL",
		"GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL",
	} {
		if !containsString(bwrapArgs, want) {
			t.Errorf("expected bwrap argv to export %s so `git commit` works inside the sandbox, got %v", want, bwrapArgs)
		}
	}

	// --setenv must appear before the "--" that ends the bwrap options, or the
	// sandbox would treat it as the command to run.
	sep := indexOf(bwrapArgs, "--")
	setenv := indexOf(bwrapArgs, "--setenv")
	if setenv < 0 || sep < 0 || setenv > sep {
		t.Errorf("expected --setenv before the bwrap option terminator, got %v", bwrapArgs)
	}
}
