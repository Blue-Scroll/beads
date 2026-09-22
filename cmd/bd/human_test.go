package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/types"
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

func TestHumanRespondRequiresResponseFlag(t *testing.T) {
	flag := humanRespondCmd.Flags().Lookup("response")
	if flag == nil {
		t.Fatal("respond command should have --response flag")
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
