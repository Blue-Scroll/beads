package dolt

import (
	"fmt"
	"regexp"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage/issueops"
	"github.com/steveyegge/beads/internal/types"
)

// Two planner guards for vn-cdrw5n3. Both EXPLAIN the production statement
// text (single-sourced from the code that runs it) on a real Dolt and assert
// the ACCESS PATH, because the plans they pin are decided by the Dolt
// planner's shape heuristics (a managed server has no table statistics) and
// a merge join over two primary keys reads as the cheapest plan to it
// whenever it is possible at all. The fast-tier string tests beside the
// production code pin the SQL shape; these pin what the engine makes of it.
// They skip rather than fail when the EXPLAIN format is unrecognizable.

// TestIterIssuesListPlanKeepsStatusIndex: a list with a status filter and a
// metadata-field filter must seek a status index on issues and never
// merge-join issues with leases. With the bare leases table in the join,
// Dolt chose LeftOuterMergeJoin over a full PK walk of issues and ran the
// JSON probe on every row (71k rows, 20 s on one ledger); LeaseJoin's
// derived table removes the merge option.
//
// The key is gc.routed_to, which has no generated column, so the status
// index is the only one the plan can earn. gc.root_bead_id has its own
// index (vn-s54d6fy) and its own test below.
func TestIterIssuesListPlanKeepsStatusIndex(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedPlanIssues(t, store, "pl-list", 40)

	filter := types.IssueFilter{
		Statuses:       []types.Status{types.StatusOpen, types.StatusInProgress},
		MetadataFields: map[string]string{"gc.routed_to": "pl-list-pool"},
	}
	where, args, err := issueops.BuildIssueFilterClauses("", filter, issueops.IssuesFilterTables)
	if err != nil {
		t.Fatalf("build filter: %v", err)
	}
	prod := iterIssuesSQL("WHERE "+strings.Join(where, " AND "), "")
	lits := make([]string, len(args))
	for i, a := range args {
		lits[i] = fmt.Sprintf("'%v'", a)
	}
	plan := explainPlan(t, ctx, store.db, literalizeParams(prod, lits...))
	if !looksLikeDoltPlan(plan) {
		t.Skipf("EXPLAIN output not in a recognized Dolt plan format, skipping plan assertion; plan=\n%s", plan)
	}
	if strings.Contains(plan, "MergeJoin") {
		t.Fatalf("issues list merge-joins leases: that walks every issues row in PK order and drops the status index.\nplan:\n%s", plan)
	}
	if !statusIndexSeek.MatchString(plan) {
		t.Fatalf("issues list does not seek an index on issues.status.\nplan:\n%s", plan)
	}
}

// TestIterIssuesRootMembersPlanSeeksRootIndex is the gc DirectMembers shape
// (vn-s54d6fy): every bead whose gc.root_bead_id is one root, closed
// included, so no status filter can narrow it. Before migration 0067 this was
// a full scan with a JSON probe per row (10 to 15 s on a 71k-row ledger). It
// must now seek idx_issues_gc_root_bead_id, and never merge-join leases.
func TestIterIssuesRootMembersPlanSeeksRootIndex(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	seedPlanIssues(t, store, "pl-root", 40)

	filter := types.IssueFilter{
		MetadataFields: map[string]string{"gc.root_bead_id": "pl-root-0"},
	}
	where, args, err := issueops.BuildIssueFilterClauses("", filter, issueops.IssuesFilterTables)
	if err != nil {
		t.Fatalf("build filter: %v", err)
	}
	prod := iterIssuesSQL("WHERE "+strings.Join(where, " AND "), "")
	lits := make([]string, len(args))
	for i, a := range args {
		lits[i] = fmt.Sprintf("'%v'", a)
	}
	plan := explainPlan(t, ctx, store.db, literalizeParams(prod, lits...))
	if !looksLikeDoltPlan(plan) {
		t.Skipf("EXPLAIN output not in a recognized Dolt plan format, skipping plan assertion; plan=\n%s", plan)
	}
	if strings.Contains(plan, "MergeJoin") {
		t.Fatalf("root-members list merge-joins leases.\nplan:\n%s", plan)
	}
	if !strings.Contains(plan, "index: [issues.gc_root_bead_id]") {
		t.Fatalf("root-members list does not seek idx_issues_gc_root_bead_id; that is a full scan with a JSON probe per row.\nplan:\n%s", plan)
	}
}

// statusIndexSeek matches an IndexedTableAccess index list that includes
// issues.status (idx_issues_status_updated_at or idx_issues_is_blocked).
var statusIndexSeek = regexp.MustCompile(`index: \[[^\]]*issues\.status`)

// TestBatchedMarkBlockedPlanSeeksTheBatch: the batched is_blocked mark must
// read the batch's dependency rows through idx_dependencies_issue and look
// each target up by key, never merge-join dependencies with issues or wisps
// over their whole tables. Flat, four of the five union legs did exactly
// that (12.8 s for one id on a 108k-row dependencies table); the DISTINCT
// derived table in each scoped leg is what keeps the planner on the seek.
func TestBatchedMarkBlockedPlanSeeksTheBatch(t *testing.T) {
	store, cleanup := setupTestStore(t)
	defer cleanup()
	ctx, cancel := testContext(t)
	defer cancel()

	ids := seedPlanIssues(t, store, "pl-mark", 40)

	stmt, args := issueops.BatchedMarkBlockedStatementForIssues(ids[:3])
	lits := make([]string, len(args))
	for i, a := range args {
		lits[i] = fmt.Sprintf("'%v'", a)
	}
	plan := explainPlan(t, ctx, store.db, literalizeParams(stmt, lits...))
	if !looksLikeDoltPlan(plan) {
		t.Skipf("EXPLAIN output not in a recognized Dolt plan format, skipping plan assertion; plan=\n%s", plan)
	}
	if strings.Contains(plan, "MergeJoin") {
		t.Fatalf("batched mark merge-joins a whole table.\nplan:\n%s", plan)
	}
	if !strings.Contains(plan, "index: [dependencies.issue_id") {
		t.Fatalf("batched mark does not seek idx_dependencies_issue for the batch.\nplan:\n%s", plan)
	}
	for _, full := range []string{"index: [dependencies.depends_on_issue_id]", "index: [dependencies.depends_on_wisp_id]"} {
		if strings.Contains(plan, full) {
			t.Fatalf("batched mark walks dependencies by %s, the merge-join shape.\nplan:\n%s", full, plan)
		}
	}
}

// seedPlanIssues creates n open issues named prefix-<i>, each blocked by the
// previous one, so the dependency and issues tables hold enough rows for the
// planner to make a choice. Returns the ids in creation order.
func seedPlanIssues(t *testing.T, store *DoltStore, prefix string, n int) []string {
	t.Helper()
	ctx, cancel := testContext(t)
	defer cancel()
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		id := fmt.Sprintf("%s-%d", prefix, i)
		iss := &types.Issue{ID: id, Title: id, Status: types.StatusOpen, Priority: 2, IssueType: types.TypeTask}
		if err := store.CreateIssue(ctx, iss, "tester"); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
		if i > 0 {
			dep := &types.Dependency{IssueID: id, DependsOnID: ids[i-1], Type: types.DepBlocks}
			if err := store.AddDependency(ctx, dep, "tester"); err != nil {
				t.Fatalf("add dependency %s -> %s: %v", id, ids[i-1], err)
			}
		}
		ids = append(ids, id)
	}
	return ids
}
