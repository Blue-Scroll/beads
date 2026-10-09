package issueops

import "testing"

// TestDecodeCountsDepsSortsEdges pins the order a counts row's edges come back
// in. JSON_ARRAYAGG has none, and on Dolt the two forms of the counts query
// handed one row's edges back in different orders (vn-ws7tu8m). Neuter: drop
// the sort in decodeCountsDeps and this goes red.
func TestDecodeCountsDepsSortsEdges(t *testing.T) {
	t.Parallel()

	raw := `[
		{"issue_id":"vn-a","depends_on_id":"vn-q","type":"blocks","created_at":"2026-10-09T06:00:05Z"},
		{"issue_id":"vn-a","depends_on_id":"vn-9","type":"parent-child","created_at":"2026-10-09T05:59:10Z"},
		{"issue_id":"vn-a","depends_on_id":"vn-9","type":"blocks","created_at":"2026-10-09T05:58:00Z"}
	]`
	deps, err := decodeCountsDeps(raw)
	if err != nil {
		t.Fatalf("decodeCountsDeps: %v", err)
	}
	want := []string{"vn-9 blocks", "vn-9 parent-child", "vn-q blocks"}
	if len(deps) != len(want) {
		t.Fatalf("got %d edges, want %d", len(deps), len(want))
	}
	for i, d := range deps {
		if got := d.DependsOnID + " " + string(d.Type); got != want[i] {
			t.Errorf("edge %d = %q, want %q", i, got, want[i])
		}
	}

	if _, err := decodeCountsDeps(`not json`); err == nil {
		t.Error("decodeCountsDeps accepted text that is not JSON")
	}
}
