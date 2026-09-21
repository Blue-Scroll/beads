//go:build unix

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/steveyegge/beads/internal/types"
)

func TestCreateCheckSkipReason(t *testing.T) {
	task := &types.Issue{ID: "bd-1", IssueType: types.TypeTask}
	cases := []struct {
		name     string
		issue    *types.Issue
		command  string
		hooksOff bool
		embedded bool
		want     string
	}{
		{"a task with a command runs", task, "check", false, false, ""},
		{"no command never runs", task, "", false, false, "not configured"},
		{"--no-hooks turns it off", task, "check", true, false, "--no-hooks"},
		{"an ephemeral issue is skipped", &types.Issue{IssueType: types.TypeTask, Ephemeral: true}, "check", false, false, "ephemeral issue"},
		{"a no-history issue is skipped", &types.Issue{IssueType: types.TypeTask, NoHistory: true}, "check", false, false, "ephemeral issue"},
		{"a message is not work", &types.Issue{IssueType: types.TypeMessage}, "check", false, false, "not a work type"},
		{"a molecule is not work", &types.Issue{IssueType: types.TypeMolecule}, "check", false, false, "not a work type"},
		{"a custom type is not work", &types.Issue{IssueType: "step"}, "check", false, false, "not a work type"},
		{"a bug runs", &types.Issue{IssueType: types.TypeBug}, "check", false, false, ""},
		{"an epic runs", &types.Issue{IssueType: types.TypeEpic}, "check", false, false, ""},
		// Embedded is checked last, so only a work issue that WOULD have been
		// checked reports it. runCreateCheck prints DARK for exactly this.
		{"embedded mode cannot run it", task, "check", false, true, "embedded mode"},
		{"embedded mode stays quiet for a message", &types.Issue{IssueType: types.TypeMessage}, "check", false, true, "not a work type"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := createCheckSkipReason(tc.issue, tc.command, tc.hooksOff, tc.embedded); got != tc.want {
				t.Fatalf("createCheckSkipReason() = %q, want %q", got, tc.want)
			}
		})
	}
}

// writeCheck writes an executable shell script and returns its path.
func writeCheck(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "check.sh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestRunCreateCheckCommand(t *testing.T) {
	t.Run("the new ID arrives as the last argument, and the output is passed through", func(t *testing.T) {
		check := writeCheck(t, `echo "args: $*"`)
		var out bytes.Buffer
		runCreateCheckCommand(check+" --bead", "bd-7x", 5*time.Second, &out)
		if got := out.String(); got != "args: --bead bd-7x\n" {
			t.Fatalf("output = %q", got)
		}
	})

	t.Run("an ID with spaces or quotes stays one argument", func(t *testing.T) {
		check := writeCheck(t, `echo "$#:$1"`)
		var out bytes.Buffer
		runCreateCheckCommand(check, `a b "c"`, 5*time.Second, &out)
		if got := out.String(); got != "1:a b \"c\"\n" {
			t.Fatalf("output = %q", got)
		}
	})

	t.Run("the check's own exit codes are its business, bd adds nothing", func(t *testing.T) {
		for _, code := range []string{"0", "3", "10"} {
			check := writeCheck(t, "echo judged; exit "+code)
			var out bytes.Buffer
			runCreateCheckCommand(check, "bd-1", 5*time.Second, &out)
			if got := out.String(); got != "judged\n" {
				t.Fatalf("exit %s: output = %q", code, got)
			}
		}
	})

	t.Run("a missing check says DARK", func(t *testing.T) {
		var out bytes.Buffer
		runCreateCheckCommand(filepath.Join(t.TempDir(), "no-such-check"), "bd-1", 5*time.Second, &out)
		if !strings.Contains(out.String(), "bd: create check DARK: sh could not run it (exit 127). bd-1 was created as normal") {
			t.Fatalf("output = %q", out.String())
		}
	})

	t.Run("a check that is not executable says DARK", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "check.sh")
		if err := os.WriteFile(p, []byte("#!/bin/sh\necho hi\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		runCreateCheckCommand(p, "bd-1", 5*time.Second, &out)
		if !strings.Contains(out.String(), "DARK: sh could not run it (exit 126)") {
			t.Fatalf("output = %q", out.String())
		}
	})

	t.Run("a hung check is stopped at the timeout, children too, and says DARK", func(t *testing.T) {
		// The child sleeps in the background holding the output pipe. Killing
		// only sh would leave it, and Wait would block on the pipe.
		pidFile := filepath.Join(t.TempDir(), "child.pid")
		check := writeCheck(t, "sleep 30 & echo $! > "+pidFile+"; sleep 30")
		var out bytes.Buffer
		start := time.Now()
		runCreateCheckCommand(check, "bd-1", 2*time.Second, &out)
		if took := time.Since(start); took > 8*time.Second {
			t.Fatalf("took %s, the timeout did not stop the check", took)
		}
		raw, err := os.ReadFile(pidFile)
		if err != nil {
			t.Fatal(err)
		}
		pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err != nil {
			t.Fatal(err)
		}
		// Signal 0 only asks "is it alive". The kill is async, so allow a moment.
		deadline := time.Now().Add(2 * time.Second)
		for syscall.Kill(pid, 0) == nil {
			if time.Now().After(deadline) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
				t.Fatalf("the check's child %d outlived the timeout", pid)
			}
			time.Sleep(20 * time.Millisecond)
		}
		if !strings.Contains(out.String(), "DARK: it ran past 2s and was stopped. bd-1 was created as normal") {
			t.Fatalf("output = %q", out.String())
		}
	})
}
