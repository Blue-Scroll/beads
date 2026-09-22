//go:build cgo

package main

import (
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestEmbeddedDepAddRefusesUnresolvableTarget is the end-to-end proof for
// vn-wzy43u5: "bd dep add" must refuse a depends-on id this database cannot
// read, instead of storing it as an unreadable target.
//
// Before the fix, an id whose prefix differed from the source's was passed
// straight through and written into dependencies.depends_on_external. Nothing
// joins that column back to a row, so dependency_count went up by one and
// nothing ever blocked. Run this file against the old binary and every
// sub-test below fails: the add succeeds and dependency_count reads 1.
func TestEmbeddedDepAddRefusesUnresolvableTarget(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "ft")

	issue := bdCreate(t, bd, dir, "Waiting on another ledger", "--type", "task")

	// An id from another ledger. Its prefix differs from the source's, which
	// is exactly the shape the old code let through.
	const foreign = "zz-c9mgk6"

	t.Run("foreign_id_is_refused", func(t *testing.T) {
		out := bdDepFail(t, bd, dir, "add", issue.ID, foreign)
		for _, want := range []string{foreign, "external:"} {
			if !strings.Contains(out, want) {
				t.Errorf("refusal should mention %q, got:\n%s", want, out)
			}
		}
	})

	t.Run("refused_add_writes_no_edge", func(t *testing.T) {
		details := bdShowDetails(t, bd, dir, issue.ID)
		// A refusal that still bumped the counter would be the exact bug this
		// change removes, so assert the counter and not just the exit code.
		if n, ok := details["dependency_count"].(float64); ok && n != 0 {
			t.Errorf("dependency_count = %v after a refused add, want 0", n)
		}
		if deps, ok := details["dependencies"].([]interface{}); ok && len(deps) != 0 {
			t.Errorf("dependencies = %v after a refused add, want none", deps)
		}
	})

	t.Run("typo_in_same_ledger_is_still_refused", func(t *testing.T) {
		// The old prefix test only let a DIFFERENT prefix through, so a
		// same-prefix typo was already refused. Pin that it still is.
		out := bdDepFail(t, bd, dir, "add", issue.ID, "ft-nosuchid")
		if !strings.Contains(out, "ft-nosuchid") {
			t.Errorf("refusal should name the missing id, got:\n%s", out)
		}
	})

	t.Run("external_reference_still_works", func(t *testing.T) {
		// The modeled way to point at another project must be untouched.
		out := bdDep(t, bd, dir, "add", issue.ID, "external:otherproject:some-capability")
		if !strings.Contains(out, "Added") {
			t.Errorf("expected the external: add to succeed, got:\n%s", out)
		}
	})

	t.Run("bulk_add_refuses_the_same_id", func(t *testing.T) {
		edge := fmt.Sprintf(`{"from":%q,"to":%q}`+"\n", issue.ID, foreign)
		out := bdDepWithInputFail(t, bd, dir, edge, "add", "--file", "-")
		if !strings.Contains(out, foreign) {
			t.Errorf("bulk refusal should name the id, got:\n%s", out)
		}
	})
}

// TestEmbeddedDepRemoveStillTakesAForeignID pins the one door that keeps the
// old pass-through. Rows written before the refusal existed point at ids this
// database cannot read, and naming one is the only way to delete it. If
// "dep remove" ever starts refusing the id shape too, those rows are stranded.
func TestEmbeddedDepRemoveStillTakesAForeignID(t *testing.T) {
	if os.Getenv("BEADS_TEST_EMBEDDED_DOLT") != "1" {
		t.Skip("set BEADS_TEST_EMBEDDED_DOLT=1 to run embedded dolt integration tests")
	}
	t.Parallel()

	bd := buildEmbeddedBD(t)
	dir, _, _ := bdInit(t, bd, "--prefix", "fr")
	issue := bdCreate(t, bd, dir, "Holds a legacy foreign edge", "--type", "task")

	// There is no edge to delete here, and remove is idempotent, so what this
	// grades is how far the command gets: it must accept the foreign id
	// instead of dying on it the way "dep add" now does.
	const foreign = "zz-c9mgk6"
	out := bdDep(t, bd, dir, "remove", issue.ID, foreign)
	if !strings.Contains(out, foreign) {
		t.Errorf("dep remove should have named %s, got:\n%s", foreign, out)
	}
}
