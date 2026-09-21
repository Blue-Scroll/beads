package main

// create_check.go: an optional check that runs right after `bd create` makes
// one work issue, so every creator gets it without having to remember it.
//
// Set it in config.yaml (never the database, see YamlOnlyKeys):
//
//	create.check-command: "/path/to/check --bead"
//	create.check-timeout: 60s   # optional, default 60s
//
// bd runs `sh -c '<command> "$1"' <new-id>`, so the new issue's ID is the
// last argument. The town uses this to ask whether an open issue already
// covers the new one (jev-dupe-check.sh). It was a separate script first, and
// only the creators who remembered to run it got checked.
//
// THE CONTRACT. The check ADVISES. It can never change what create does:
//
//   - It runs only after the issue is safely created.
//   - bd's stdout is never touched. Everything the check prints goes to
//     stderr, so a caller reading `--json` or `--silent` sees exactly the
//     bytes it saw before this existed.
//   - bd's exit code is never touched. A check that fails, hangs, or is
//     missing still leaves create exiting 0.
//   - When the check says nothing because it could not run (it timed out, it
//     could not start, sh could not find it), bd prints one line with the
//     word DARK. A check that went quiet must never look like a check that
//     found nothing.
//
// A plain on_create hook cannot do this job. Hooks run in the background with
// their output thrown away, and a short-lived `bd create` often exits before
// the hook finishes.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/steveyegge/beads/internal/config"
	"github.com/steveyegge/beads/internal/types"
)

const (
	createCheckCommandKey     = "create.check-command"
	createCheckTimeoutKey     = "create.check-timeout"
	defaultCreateCheckTimeout = 60 * time.Second
)

// createCheckTypes are the issue types a person files as work. Everything
// else is machinery (messages, molecules, gates, events, agents, formula
// steps and other custom types), made by tools many times a minute. Checking
// those would slow every mail and every molecule pour, and a message is never
// a duplicate of a task.
var createCheckTypes = map[types.IssueType]bool{
	types.TypeBug:      true,
	types.TypeFeature:  true,
	types.TypeTask:     true,
	types.TypeEpic:     true,
	types.TypeChore:    true,
	types.TypeDecision: true,
	types.TypeSpike:    true,
	types.TypeStory:    true,
}

// createCheckSkipReason says why the check should NOT run for this issue, or
// "" when it should. It is pure so the rules are testable without a store.
func createCheckSkipReason(issue *types.Issue, command string, hooksOff, embedded bool) string {
	switch {
	case issue == nil || command == "":
		return "not configured"
	case hooksOff:
		return "--no-hooks"
	case issue.Ephemeral || issue.NoHistory:
		return "ephemeral issue"
	case !createCheckTypes[issue.IssueType.Normalize()]:
		return "not a work type"
	case embedded:
		// Embedded Dolt commits in PersistentPostRun, after RunE, and holds
		// the store lock until bd exits. A check that reads or writes the new
		// issue through a second bd would see nothing, or wait on the lock
		// until it timed out.
		return "embedded mode"
	}
	return ""
}

// runCreateCheck runs the configured check for a just-created issue. It never
// returns an error on purpose: see the contract at the top of this file.
// Call it AFTER the create's own output is printed.
func runCreateCheck(issue *types.Issue) {
	command := strings.TrimSpace(config.GetString(createCheckCommandKey))
	reason := createCheckSkipReason(issue, command, config.GetBool("no-hooks"), isEmbeddedMode())
	if reason == "embedded mode" {
		// Configured but unable to run: say so, so it does not read as clean.
		fmt.Fprintf(os.Stderr, "bd: create check DARK: %s is set, but it only runs against a Dolt server. %s was created as normal; nothing was checked.\n", createCheckCommandKey, issue.ID)
		return
	}
	if reason != "" {
		return
	}
	timeout := config.GetDuration(createCheckTimeoutKey)
	if timeout <= 0 {
		timeout = defaultCreateCheckTimeout
	}
	runCreateCheckCommand(command, issue.ID, timeout, os.Stderr)
}

// runCreateCheckCommand runs one check and writes everything it prints, plus
// bd's own DARK line when it could not run, to out.
func runCreateCheckCommand(command, id string, timeout time.Duration, out io.Writer) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// "$1" is quoted inside the script, so the ID reaches the check as one
	// argument whatever it contains. The command itself is trusted text from
	// config.yaml, the same trust bd gives a hook file.
	// #nosec G204 -- command comes from the local config.yaml, never the database
	cmd := exec.CommandContext(ctx, "sh", "-c", command+` "$1"`, "bd-create-check", id)
	cmd.Stdout = out
	cmd.Stderr = out
	setCreateCheckProcessGroup(cmd)
	// When out is not a file, Wait also waits for every child still holding
	// the pipe. Cap that, so a check that leaves a child behind cannot hang
	// create past its own timeout.
	cmd.WaitDelay = 2 * time.Second

	err := cmd.Run()
	if dark := createCheckDarkReason(ctx, err, timeout); dark != "" {
		fmt.Fprintf(out, "bd: create check DARK: %s. %s was created as normal; nothing was checked.\n", dark, id)
	}
}

// createCheckDarkReason turns how the check ended into bd's DARK reason, or
// "" when the check ran and its own output already said what it found. Any
// exit code the check chose (0, 10 for a hit, 3 for its own dark) is its own
// business and bd adds nothing.
func createCheckDarkReason(ctx context.Context, err error, timeout time.Duration) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return fmt.Sprintf("it ran past %s and was stopped", timeout)
	}
	if err == nil {
		return ""
	}
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		return fmt.Sprintf("it could not start (%v)", err)
	}
	// sh uses 126 and 127 for "found but not runnable" and "not found".
	// Those mean the check never ran at all.
	if code := exitErr.ExitCode(); code == 126 || code == 127 {
		return fmt.Sprintf("sh could not run it (exit %d)", code)
	}
	return ""
}
