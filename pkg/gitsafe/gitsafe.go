// Package gitsafe builds git invocations that stay safe when the repository's
// configuration is writable by an untrusted party.
//
// The threat this exists for: a bare repository cache bound read-write inside a
// sandbox, so an agent can write the cache's config, and a linked worktree
// shares that config. Any git the host runs afterwards — `worktree add`,
// `diff`, `status`, `rev-parse`, `prune` — then reads what the agent wrote, with
// the host user's privileges.
//
// Two classes of setting have to be handled differently:
//
//   - Fixed keys (core.hooksPath, core.fsmonitor, diff.external, pager,
//     credential.helper, core.gitProxy, protocol.file). A blanket `-c` override
//     always wins over every config file, so one flag per key is enough.
//   - Dynamic keys (filter.<name>.smudge and friends). The name is chosen by
//     whoever wrote the config, so no blanket flag can disable them: they have
//     to be enumerated from the repository and neutralized one by one. They
//     matter because `worktree add` performs a checkout, and checkout runs the
//     smudge filter.
//
// System and global config are disabled through the environment (Env), because
// the host's own ~/.gitconfig is not part of the trust boundary we can reason
// about here and a poisoned one reintroduces every fixed key.
package gitsafe

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// fixedArgs are the overrides for the config keys git resolves by name. -c has
// the highest precedence, so these override system, global and local config.
func fixedArgs() []string {
	return []string{
		"-c", "core.hooksPath=" + os.DevNull,
		"-c", "core.fsmonitor=false",
		"-c", "core.pager=cat",
		// diff.external is emptied rather than replaced: it is a fail-closed
		// choice, since git refuses to run an empty external diff instead of
		// falling back to one. That is why Command refuses a bare `diff`.
		"-c", "diff.external=",
		"-c", "credential.helper=",
		"-c", "core.gitProxy=",
		"-c", "protocol.file.allow=never",
		"--no-optional-locks",
	}
}

// filterAttrs are the per-driver settings that name a program to run.
var filterAttrs = []string{"clean", "smudge", "process"}

// Overrides returns only the environment variables that stop git from loading
// a hostile system or global config. Callers that manage their own environment
// (the settlement evaluator allowlists it, for instance) append these to it
// instead of using Env.
func Overrides() []string {
	return []string{
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
	}
}

// Env returns the parent environment plus Overrides. Everything else is kept:
// this package is about where git reads configuration from, not about scrubbing
// the environment.
func Env() []string {
	return append(append([]string{}, os.Environ()...), Overrides()...)
}

// filterDrivers lists the filter driver names configured in dir. It follows
// include.path on purpose: an included file can define a driver, and skipping it
// would hand back an incomplete list, which is exactly the failure mode this
// function exists to prevent.
func filterDrivers(dir string) ([]string, error) {
	cmd := exec.Command("git", "--no-optional-locks", "-C", dir, "config",
		"--get-regexp", `^filter\..*`)
	cmd.Env = Env()
	var stderr strings.Builder
	cmd.Stderr = &stderr

	out, err := cmd.Output()
	if err != nil {
		// git config exits 1 when nothing matched, which is the common case.
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		// A directory that is not a repository has no config to read, so there
		// is nothing to neutralize. Reporting it as an error here would change
		// what the caller sees: the git command it goes on to build fails with
		// this same "not a git repository" message.
		if strings.Contains(stderr.String(), "not a git repository") {
			return nil, nil
		}
		return nil, fmt.Errorf("enumerate filter drivers in %s: %w%s", dir, err, stderrSuffix(stderr.String()))
	}

	seen := map[string]struct{}{}
	drivers := make([]string, 0, 4)
	for _, line := range strings.Split(string(out), "\n") {
		key, _, found := strings.Cut(strings.TrimSpace(line), " ")
		if !found {
			continue
		}
		name, ok := driverName(key)
		if !ok {
			continue
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		drivers = append(drivers, name)
	}
	return drivers, nil
}

// driverName extracts the driver name from a filter.<name>.<attr> config key.
func driverName(key string) (string, bool) {
	rest, found := strings.CutPrefix(key, "filter.")
	if !found || rest == "" {
		return "", false
	}
	idx := strings.LastIndex(rest, ".")
	if idx <= 0 || idx == len(rest)-1 {
		return "", false
	}
	attr := rest[idx+1:]
	for _, known := range filterAttrs {
		if attr == known {
			return rest[:idx], true
		}
	}
	return "", false
}

func stderrSuffix(s string) string {
	if s = strings.TrimSpace(s); s == "" {
		return ""
	}
	return ": " + s
}

// Argv returns the full argument vector for a hardened git invocation in dir,
// including the program-neutralization overrides.
//
// It is exposed separately from Command for callers that build the *exec.Cmd
// themselves (the runner injects a command factory to assert argv in tests).
func Argv(dir string, args ...string) ([]string, error) {
	if err := checkDiffArgs(args); err != nil {
		return nil, err
	}
	drivers, err := filterDrivers(dir)
	if err != nil {
		return nil, err
	}

	full := make([]string, 0, 4*len(fixedArgs())+6*len(drivers)+2+len(args))
	full = append(full, fixedArgs()...)
	for _, name := range drivers {
		for _, attr := range filterAttrs {
			// Emptying the command and relaxing required turns every driver
			// into a pass-through: checkout still copies the blob verbatim, but
			// nothing is executed. Verified against a poisoned driver.
			full = append(full, "-c", fmt.Sprintf("filter.%s.%s=", name, attr))
		}
		full = append(full, "-c", fmt.Sprintf("filter.%s.required=false", name))
	}
	if dir != "" {
		full = append(full, "-C", dir)
	}
	return append(full, args...), nil
}

// Command builds a git invocation for dir with every config-driven program path
// neutralized.
//
// It fails rather than guessing when the filter drivers in dir cannot be
// enumerated: an incomplete list would silently restore the escape this
// package exists to close.
func Command(ctx context.Context, dir string, args ...string) (*exec.Cmd, error) {
	full, err := Argv(dir, args...)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = Env()
	return cmd, nil
}

// checkDiffArgs refuses a diff invocation that would hit the external diff
// machinery. With diff.external emptied git fails hard rather than diffing, so
// a `diff` without these flags would look like a broken runner instead of a
// neutralized one.
func checkDiffArgs(args []string) error {
	if len(args) == 0 || args[0] != "diff" {
		return nil
	}
	noExt, noTextconv := false, false
	for _, a := range args[1:] {
		switch a {
		case "--no-ext-diff":
			noExt = true
		case "--no-textconv":
			noTextconv = true
		}
	}
	if !noExt || !noTextconv {
		return errors.New("gitsafe: a git diff must pass --no-ext-diff and --no-textconv")
	}
	return nil
}
