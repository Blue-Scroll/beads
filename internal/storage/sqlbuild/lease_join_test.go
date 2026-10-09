package sqlbuild

import (
	"strings"
	"testing"
)

// LeaseJoin reads leases through a derived table so the Dolt planner cannot
// merge-join issues and leases on their primary keys, which is the plan that
// walks every issues row and throws the status index away (vn-cdrw5n3). The
// shape is load-bearing; this pins it at the fast tier. The planner side is
// guarded by TestIterIssuesListPlanKeepsStatusIndex in internal/storage/dolt.
func TestLeaseJoinReadsLeasesThroughADerivedTable(t *testing.T) {
	got := LeaseJoin("issues")

	if strings.Contains(got, "LEFT JOIN leases ") {
		t.Fatalf("LeaseJoin joins the bare leases table, which lets the planner merge-join it on the PK: %s", got)
	}
	if !strings.HasPrefix(got, "LEFT JOIN (SELECT ") || !strings.Contains(got, " FROM leases) leases ON leases.issue_id = issues.id") {
		t.Fatalf("LeaseJoin shape changed; want `LEFT JOIN (SELECT ... FROM leases) leases ON leases.issue_id = issues.id`, got: %s", got)
	}

	// Every leases.<col> the select lists read must come out of the derived
	// table, or the first query to hydrate a lease fails at the engine.
	for _, col := range strings.Split(LeaseSelectColumns, ",") {
		col = strings.TrimPrefix(strings.TrimSpace(col), "leases.")
		if !strings.Contains(leaseTableColumns, col) {
			t.Errorf("LeaseSelectColumns reads leases.%s but the derived table in LeaseJoin does not project it", col)
		}
	}
	if !strings.Contains(leaseTableColumns, "issue_id") {
		t.Error("the derived table must project issue_id, the join key")
	}
}
