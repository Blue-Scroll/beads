package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/workapi"
)

func TestPrintHumanStats(t *testing.T) {
	tests := []struct {
		name   string
		issues []*types.Issue
		// We just verify no panic; output goes to stdout
	}{
		{
			name:   "empty list",
			issues: nil,
		},
		{
			name: "mixed statuses",
			issues: []*types.Issue{
				{ID: "bd-1", Status: "open"},
				{ID: "bd-2", Status: "in_progress"},
				{ID: "bd-3", Status: "blocked"},
				{ID: "bd-4", Status: "closed", CloseReason: "Responded"},
				{ID: "bd-5", Status: "closed", CloseReason: "Dismissed: not needed"},
				{ID: "bd-6", Status: "hooked"},
			},
		},
		{
			name: "all closed responded",
			issues: []*types.Issue{
				{ID: "bd-1", Status: "closed", CloseReason: "Responded"},
				{ID: "bd-2", Status: "closed", CloseReason: "Responded"},
			},
		},
		{
			name: "all dismissed",
			issues: []*types.Issue{
				{ID: "bd-1", Status: "closed", CloseReason: "Dismissed"},
				{ID: "bd-2", Status: "closed", CloseReason: "Dismissed: stale"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Just verify no panic
			printHumanStats(tt.issues)
		})
	}
}

func TestPrintHumanListEmpty(t *testing.T) {
	var buf bytes.Buffer
	fprintHumanList(&buf, nil)
	if !strings.Contains(buf.String(), "No human-needed beads found.") {
		t.Errorf("empty list should say so, got:\n%s", buf.String())
	}
}

// An OPEN bead is the one still waiting on a person. The list used to print a
// Status line for every OTHER state, so a count built by grepping "Status:"
// counted every answered bead and none of the waiting ones (hq-vjm31n).
func TestPrintHumanListPrintsStatusForOpenBeads(t *testing.T) {
	issues := []*types.Issue{
		{ID: "bd-1", Title: "Waiting on Casey", Status: types.StatusOpen, Priority: 1},
		{ID: "bd-2", Title: "Already answered", Status: types.StatusClosed, Priority: 2},
	}

	var buf bytes.Buffer
	fprintHumanList(&buf, issues)
	out := buf.String()

	if !strings.Contains(out, "Status: open") {
		t.Errorf("an open bead must print its status, got:\n%s", out)
	}
	if got := strings.Count(out, "Status:"); got != len(issues) {
		t.Errorf("every bead needs one Status line: want %d, got %d\n%s", len(issues), got, out)
	}
}

// P0 is the most urgent priority. It used to print no Priority line at all,
// because the printer treated the zero value as "not set".
func TestPrintHumanListPrintsP0(t *testing.T) {
	var buf bytes.Buffer
	fprintHumanList(&buf, []*types.Issue{
		{ID: "bd-1", Title: "Production is down", Status: types.StatusOpen, Priority: 0},
	})
	if !strings.Contains(buf.String(), "Priority: P0") {
		t.Errorf("P0 must print, got:\n%s", buf.String())
	}
}

// The header answers "how many decisions are waiting?" so nobody has to count
// rows. in_progress, blocked and deferred beads are waiting too: only closed
// is answered.
func TestPrintHumanListSummaryCountsEveryWaitingState(t *testing.T) {
	issues := []*types.Issue{
		{ID: "bd-1", Title: "a", Status: types.StatusOpen},
		{ID: "bd-2", Title: "b", Status: types.StatusOpen},
		{ID: "bd-3", Title: "c", Status: types.StatusInProgress},
		{ID: "bd-4", Title: "d", Status: types.StatusBlocked},
		{ID: "bd-5", Title: "e", Status: types.StatusDeferred},
		{ID: "bd-6", Title: "f", Status: types.StatusClosed},
	}

	var buf bytes.Buffer
	fprintHumanList(&buf, issues)
	out := buf.String()

	for _, want := range []string{
		"Waiting on a human: 5",
		"open 2",
		"in_progress 1",
		"blocked 1",
		"deferred 1",
		"Already answered: 1",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary is missing %q, got:\n%s", want, out)
		}
	}
}

// --waiting keeps every bead that nobody has answered. --status=open cannot do
// this job: it drops the in_progress and blocked ones.
func TestFilterWaitingOnHuman(t *testing.T) {
	issues := []*types.Issue{
		{ID: "bd-1", Status: types.StatusOpen},
		{ID: "bd-2", Status: types.StatusInProgress},
		{ID: "bd-3", Status: types.StatusBlocked},
		{ID: "bd-4", Status: types.StatusDeferred},
		{ID: "bd-5", Status: types.StatusHooked},
		{ID: "bd-6", Status: types.StatusClosed},
	}

	kept := filterWaitingOnHuman(issues)
	if len(kept) != 5 {
		t.Fatalf("want 5 waiting beads, got %d", len(kept))
	}
	for _, issue := range kept {
		if issue.Status == types.StatusClosed {
			t.Errorf("a closed bead is answered, it must not be kept: %s", issue.ID)
		}
	}
}

func TestHumanListHasWaitingFlag(t *testing.T) {
	if humanListCmd.Flags().Lookup("waiting") == nil {
		t.Fatal("list command should have --waiting flag")
	}
}

func TestHumanCmdSubcommands(t *testing.T) {
	// Verify all subcommands are registered
	subCmds := humanCmd.Commands()
	names := make([]string, len(subCmds))
	for i, cmd := range subCmds {
		names[i] = cmd.Name()
	}
	joined := strings.Join(names, ",")

	for _, expected := range []string{"list", "respond", "dismiss", "stats"} {
		if !strings.Contains(joined, expected) {
			t.Errorf("missing subcommand %q in human command", expected)
		}
	}
}

// TestHumanRespondDismissArgs pins the Args policy for respond and dismiss:
// an issue ID is required and trailing args are free text, not extra IDs
// (MinimumNArgs(1), not ExactArgs(1)). End-to-end coverage lives in the
// embedded tests, which are env-gated — this always-run check guards the
// declaration itself.
func TestHumanRespondDismissArgs(t *testing.T) {
	for _, cmd := range []*cobra.Command{humanRespondCmd, humanDismissCmd} {
		if err := cmd.Args(cmd, []string{"bd-123", "free", "text"}); err != nil {
			t.Errorf("%s should accept positional free text after the ID: %v", cmd.Name(), err)
		}
		if err := cmd.Args(cmd, []string{}); err == nil {
			t.Errorf("%s should still require an issue ID", cmd.Name())
		}
	}
}

func TestHumanListFilter(t *testing.T) {
	cfg := workapi.ListConfig{
		CustomStatuses: []types.CustomStatus{
			{Name: "archived", Category: types.CategoryDone},
			{Name: "review", Category: types.CategoryActive},
		},
	}

	t.Run("default hides the canonical done/frozen statuses", func(t *testing.T) {
		filter, err := humanListFilter("", cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		excluded := make(map[types.Status]bool)
		for _, s := range filter.ExcludeStatus {
			excluded[s] = true
		}
		for _, want := range []types.Status{types.StatusClosed, types.StatusPinned, "archived"} {
			if !excluded[want] {
				t.Errorf("default filter should exclude %q, got ExcludeStatus=%v", want, filter.ExcludeStatus)
			}
		}
		if excluded["review"] {
			t.Errorf("active-category custom status should not be excluded, got %v", filter.ExcludeStatus)
		}
		if filter.Status != nil {
			t.Errorf("default filter should not pin a status, got %v", *filter.Status)
		}
	})

	t.Run("default hides pinned beads but no bead types", func(t *testing.T) {
		filter, err := humanListFilter("", cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if filter.Pinned == nil || *filter.Pinned {
			t.Errorf("default filter should hide boolean-pinned beads, got Pinned=%v", filter.Pinned)
		}
		if filter.Limit != 0 {
			t.Errorf("human list should stay unlimited, got Limit=%d", filter.Limit)
		}
	})

	t.Run("no bead type is ever hidden", func(t *testing.T) {
		for _, status := range []string{"", "open", "all"} {
			filter, err := humanListFilter(status, cfg)
			if err != nil {
				t.Fatalf("unexpected error for status %q: %v", status, err)
			}
			if len(filter.ExcludeTypes) != 0 {
				t.Errorf("status %q: human list must not exclude bead types (gates, infra), got %v", status, filter.ExcludeTypes)
			}
			if filter.SkipWisps {
				t.Errorf("status %q: human list must show human-labeled wisps", status)
			}
			if filter.IsTemplate != nil {
				t.Errorf("status %q: human list must not hide templates, got IsTemplate=%v", status, *filter.IsTemplate)
			}
		}
	})

	t.Run("explicit status overrides default", func(t *testing.T) {
		filter, err := humanListFilter("closed", cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if filter.Status == nil || *filter.Status != types.StatusClosed {
			t.Errorf("expected Status=closed, got %v", filter.Status)
		}
		if len(filter.ExcludeStatus) != 0 {
			t.Errorf("explicit status should drop the default exclusions, got %v", filter.ExcludeStatus)
		}
	})

	t.Run("all shows every status", func(t *testing.T) {
		filter, err := humanListFilter("all", cfg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if filter.Status != nil || len(filter.Statuses) != 0 || len(filter.ExcludeStatus) != 0 {
			t.Errorf("--status=all should not constrain status, got Status=%v Statuses=%v ExcludeStatus=%v",
				filter.Status, filter.Statuses, filter.ExcludeStatus)
		}
	})

	t.Run("invalid status is refused", func(t *testing.T) {
		if _, err := humanListFilter("nonesuch", cfg); err == nil {
			t.Error("expected an error for an unknown status")
		}
	})

	t.Run("always filters on human label", func(t *testing.T) {
		for _, status := range []string{"", "open", "all"} {
			filter, err := humanListFilter(status, cfg)
			if err != nil {
				t.Fatalf("unexpected error for status %q: %v", status, err)
			}
			if len(filter.Labels) != 1 || filter.Labels[0] != "human" {
				t.Errorf("expected Labels=[human] for status %q, got %v", status, filter.Labels)
			}
		}
	})
}

func TestHumanRespondTextSourceFlags(t *testing.T) {
	for _, name := range []string{"file", "stdin"} {
		if humanRespondCmd.Flags().Lookup(name) == nil {
			t.Errorf("respond command should have --%s flag", name)
		}
	}

	// --response must not be marked required: the response can also come from
	// --file, --stdin, or positional args, and cobra rejects those invocations
	// before RunE if the flag carries the required annotation.
	flag := humanRespondCmd.Flags().Lookup("response")
	if flag == nil {
		t.Fatal("respond command should have --response flag")
	}
	if len(flag.Annotations[cobra.BashCompOneRequiredFlag]) > 0 {
		t.Error("--response must not be hard-required; --file/--stdin/positional text are valid sources")
	}
}

func TestHumanDismissHasReasonFlag(t *testing.T) {
	flag := humanDismissCmd.Flags().Lookup("reason")
	if flag == nil {
		t.Fatal("dismiss command should have --reason flag")
	}
}

func TestHumanListHasStatusFlag(t *testing.T) {
	flag := humanListCmd.Flags().Lookup("status")
	if flag == nil {
		t.Fatal("list command should have --status flag")
	}
}
