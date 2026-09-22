//go:build cgo

package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// bdIssueCount returns how many issues the database holds right now. The
// create-side bug is only visible as a count: the old binary refused nothing,
// so it made the issue AND the unreadable edge.
func bdIssueCount(t *testing.T, bd, dir string) int {
	t.Helper()
	out, err := bdRunWithFlockRetry(t, bd, dir, "list", "--json", "--limit=0")
	if err != nil {
		t.Fatalf("bd list failed: %v\n%s", err, out)
	}
	s := string(out)
	start := strings.Index(s, "[")
	if start < 0 {
		t.Fatalf("no JSON array in bd list output:\n%s", s)
	}
	var issues []map[string]interface{}
	if err := json.NewDecoder(strings.NewReader(s[start:])).Decode(&issues); err != nil {
		t.Fatalf("parse bd list output: %v\nraw: %s", err, s[start:])
	}
	return len(issues)
}

// TestEmbeddedCreateRefusesUnresolvableDepTarget is the end-to-end proof for
// vn-zc0gvqh: "bd create --deps" must refuse a target this database cannot
// read, instead of creating the issue with an unreadable edge hanging off it.
//
// vn-wzy43u5 shut this door on "bd dep add" and left it open on create, which
// is the door agents are pushed through hardest: every polecat's boot policy
// tells it to file follow-up work with "bd create --deps blocked-by:<id>".
//
// Run this file against a binary built without the change and the refusal
// cases fail: the create succeeds, the issue count goes up, and the new issue
// carries a dependency_count of 1 that no reader will ever honor.
func TestEmbeddedCreateRefusesUnresolvableDepTarget(t *testing.T) {
	requireEmbeddedDolt(t)
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "cf")

	local := bdCreate(t, bd, dir, "A blocker that really exists", "--type", "task")

	// An id from another ledger. Its prefix differs from this database's,
	// which is exactly the shape the create path let through.
	const foreign = "zz-c9mgk6"

	t.Run("foreign_id_writes_no_phantom_fence", func(t *testing.T) {
		// Graded on the defect itself, not on the exit code: an old binary
		// creates the issue here, and its failure message below prints the
		// two numbers that disagree.
		before := bdIssueCount(t, bd, dir)
		issue := bdCreateAllowError(t, bd, dir, "Waiting on another ledger", "--deps", "blocked-by:"+foreign)
		if issue != nil {
			details := bdShowDetails(t, bd, dir, issue.ID)
			deps, _ := details["dependencies"].([]interface{})
			t.Fatalf("create should have been refused; it made %s with dependency_count=%v and %d readable dependencies. That counter is the phantom fence: bd ready hands this issue straight out.",
				issue.ID, details["dependency_count"], len(deps))
		}
		// The whole point of refusing before the create: a refusal that still
		// made the issue would leave a bead whose fence is missing, which is
		// worse than the phantom row it replaced.
		if after := bdIssueCount(t, bd, dir); after != before {
			t.Errorf("issue count = %d after a refused create, want %d (nothing should be created)", after, before)
		}
	})

	t.Run("the_refusal_says_what_to_do_instead", func(t *testing.T) {
		out := bdCreateFail(t, bd, dir, "Waiting on another ledger", "--deps", "blocked-by:"+foreign)
		for _, want := range []string{foreign, "external:", "Nothing was created"} {
			if !strings.Contains(out, want) {
				t.Errorf("refusal should mention %q, got:\n%s", want, out)
			}
		}
	})

	t.Run("bare_id_form_is_refused_too", func(t *testing.T) {
		// "--deps <id>" with no type means the same edge as "blocked-by:<id>".
		// Refusing one spelling and not the other would just move the hole.
		out := bdCreateFail(t, bd, dir, "Bare spelling", "--deps", foreign)
		if !strings.Contains(out, foreign) {
			t.Errorf("refusal should name the id, got:\n%s", out)
		}
	})

	t.Run("typo_in_same_ledger_is_still_refused", func(t *testing.T) {
		// A same-prefix miss was already refused, deeper down. Pin that it
		// still is, and that it now says so before anything is written.
		before := bdIssueCount(t, bd, dir)
		out := bdCreateFail(t, bd, dir, "Same-prefix typo", "--deps", "blocked-by:cf-nosuchid")
		if !strings.Contains(out, "cf-nosuchid") {
			t.Errorf("refusal should name the missing id, got:\n%s", out)
		}
		if after := bdIssueCount(t, bd, dir); after != before {
			t.Errorf("issue count = %d after a refused create, want %d", after, before)
		}
	})

	t.Run("a_real_target_still_works", func(t *testing.T) {
		// The common path. If this ever goes red the refusal is too wide.
		issue := bdCreate(t, bd, dir, "Blocked by a real bead", "--deps", "blocked-by:"+local.ID)
		details := bdShowDetails(t, bd, dir, issue.ID)
		if n, ok := details["dependency_count"].(float64); !ok || n != 1 {
			t.Errorf("dependency_count = %v, want 1", details["dependency_count"])
		}
	})

	t.Run("external_reference_still_works", func(t *testing.T) {
		// The modeled way to point at another project must be untouched.
		issue := bdCreate(t, bd, dir, "Waits on another project",
			"--deps", "depends-on:external:otherproject:some-capability")
		details := bdShowDetails(t, bd, dir, issue.ID)
		if n, ok := details["dependency_count"].(float64); !ok || n != 1 {
			t.Errorf("dependency_count = %v for an external: dep, want 1", details["dependency_count"])
		}
	})
}
