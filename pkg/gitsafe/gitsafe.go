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
// a hostile system or global config, plus the filter-driver neutralization for
// a given directory.
//
// Callers that manage their own environment (the settlement evaluator
// allowlists it, for instance) append these to it. Nothing here is a
// replacement for the caller's environment: these are additions.
func Overrides() []string {
	return []string{
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
	}
}

// filterDrivers lists the filter driver names configured in dir.
//
// Keys are read with --name-only -z rather than parsed out of "key value"
// lines: a driver name may contain spaces, and cutting a line at the first
// space would silently drop such a driver, which is a fail-open enumeration.
//
// It follows include.path on purpose: an included file can define a driver,
// and skipping it would hand back an incomplete list, which is exactly the
// failure mode this function exists to prevent.
func filterDrivers(dir string) ([]string, error) {
	cmd := exec.Command("git", "--no-optional-locks", "-C", dir, "config",
		"--get-regexp", "--name-only", "-z", `^filter\..*`)
	// Deliberately neither Env() nor the caller's environment: a GIT_DIR (or
	// GIT_WORK_TREE) would make the enumeration read a different repository
	// than the one about to be neutralized.
	cmd.Env = Sanitize(Overrides())

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
		if strings.Contains(err.Error(), "not a git repository") {
			return nil, nil
		}
		return nil, fmt.Errorf("enumerate filter drivers in %s: %w", dir, err)
	}

	seen := map[string]struct{}{}
	drivers := make([]string, 0, 4)
	for _, key := range strings.Split(string(out), "\x00") {
		name, ok := driverName(strings.TrimSuffix(key, "\n"))
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

// driverNeutralizationEnv returns the environment that disables every driver in
// drivers, using git's GIT_CONFIG_KEY_n/GIT_CONFIG_VALUE_n mechanism instead of
// -c overrides.
//
// The reason is precise: -c is parsed as name=value splitting at the FIRST
// equals sign, so a driver named "a=b" cannot be addressed that way at all.
// `-c filter.a=b.smudge=` sets the key `filter.a` to `b.smudge=`, leaving
// filter.a=b.smudge live. The environment entries are not split, and they
// outrank the repository config, so they neutralize every name git accepts.
func driverNeutralizationEnv(drivers []string) []string {
	if len(drivers) == 0 {
		return nil
	}
	keys := make([]string, 0, 4*len(drivers))
	values := make([]string, 0, 4*len(drivers))
	for _, name := range drivers {
		for _, attr := range filterAttrs {
			// Emptying the command and relaxing required turns every driver
			// into a pass-through: checkout still copies the blob verbatim, but
			// nothing is executed. Verified against a poisoned driver.
			keys = append(keys, fmt.Sprintf("filter.%s.%s", name, attr))
			values = append(values, "")
		}
		keys = append(keys, fmt.Sprintf("filter.%s.required", name))
		values = append(values, "false")
	}

	env := make([]string, 0, 2+2*len(keys))
	env = append(env, fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(keys)))
	for i, key := range keys {
		env = append(env,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, key),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, values[i]),
		)
	}
	return env
}

// repoLocationVars redirect git at a different repository, index or object
// store than the one the caller named. They are stripped from the environment
// of both the enumeration and the command, because if only one of them saw
// them, git would read a configuration nobody enumerated.
var repoLocationVars = []string{
	"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
	"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES",
	"GIT_CEILING_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM",
	"GIT_NAMESPACE", "GIT_PREFIX",
}

// Sanitize removes the variables that point git at another repository, index or
// object store. Callers that manage their own environment should apply it to
// whatever they are going to pass.
func Sanitize(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		drop := false
		for _, bad := range repoLocationVars {
			if name == bad {
				drop = true
				break
			}
		}
		if !drop {
			out = append(out, kv)
		}
	}
	return out
}

// Plan returns the argument vector and the environment additions for a
// hardened git invocation in dir.
//
// Both halves are returned together because they cannot be built apart: the
// filter-driver neutralization has to travel in the environment, so a caller
// that takes only the argv would silently lose it.
//
// The env slice is meant to be appended AFTER the caller's own environment, so
// these values win over anything the caller had (os/exec keeps the last
// assignment of a duplicated name).
func Plan(dir string, args ...string) (argv, env []string, err error) {
	if err := checkDiffArgs(args); err != nil {
		return nil, nil, err
	}
	drivers, err := filterDrivers(dir)
	if err != nil {
		return nil, nil, err
	}

	full := make([]string, 0, len(fixedArgs())+2+len(args))
	full = append(full, fixedArgs()...)
	if dir != "" {
		full = append(full, "-C", dir)
	}
	full = append(full, args...)

	additions := append(Overrides(), driverNeutralizationEnv(drivers)...)
	return full, additions, nil
}

// Command builds a git invocation for dir with every config-driven program path
// neutralized, inheriting the host environment.
//
// It fails rather than guessing when the filter drivers in dir cannot be
// enumerated: an incomplete list would silently restore the escape this
// package exists to close.
func Command(ctx context.Context, dir string, args ...string) (*exec.Cmd, error) {
	full, additions, err := Plan(dir, args...)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Env = append(Sanitize(os.Environ()), additions...)
	return cmd, nil
}

// checkDiffArgs refuses a diff invocation that would hit the external diff
// machinery. With diff.external emptied git fails hard rather than diffing, so
// a `diff` without these flags would look like a broken runner instead of a
// neutralized one.
//
// It also refuses a range operand that starts with "-": git parses such an
// operand as an option, not as a revision, so "<base>..HEAD" built from
// untrusted data could smuggle an option into the diff (for example
// "--output=/some/path..HEAD", which makes git write the diff to a file). This
// is the argument-injection class of #35, and it is refused at the package that
// exists to keep git invocations safe, so every caller is covered.
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
	for _, a := range args[1:] {
		if strings.Contains(a, "..") && strings.HasPrefix(a, "-") {
			return fmt.Errorf("gitsafe: refusing a diff range operand that would be parsed as an option: %q", a)
		}
	}
	return nil
}
