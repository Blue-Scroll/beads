package issueops

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
	"github.com/steveyegge/beads/internal/types"
)

func SearchIssuesWithCountsInTx(ctx context.Context, tx *sql.Tx, query string, filter types.IssueFilter) ([]*types.IssueWithCounts, error) {
	wispDepsExist, err := optionalTableExistsInTx(ctx, tx, "wisp_dependencies")
	if err != nil {
		return nil, fmt.Errorf("search issues with counts: wisp dependency probe: %w", err)
	}

	if filter.Ephemeral != nil && *filter.Ephemeral {
		empty, probeErr := wispsTableEmptyOrMissingInTx(ctx, tx)
		if probeErr != nil {
			return nil, fmt.Errorf("search issues with counts: ephemeral wisp probe: %w", probeErr)
		}
		if !empty && wispDepsExist {
			wisps, err := runFilterSearchQueryInTx(ctx, tx, query, filter, WispsFilterTables, true)
			if err != nil && !missingOptionalWispTable(err) {
				return nil, err
			}
			if len(wisps) > 0 {
				return finishSearchIssuesWithCounts(wisps, filter)
			}
		}
		// Fall through: the wisps tier is missing/empty or matched no rows.
		// Mirror SearchIssuesInTx / CountIssuesInTx so count-projection searches
		// also surface a durable issues-table row flagged ephemeral=1 instead of
		// dropping it. Use the same IssuesFilterTables query the non-ephemeral
		// path uses, keeping the GH#4387 count/list cardinality parity for
		// searches that project counts (e.g. `bd search --counts --include-infra`).
		out, err := runFilterSearchQueryInTx(ctx, tx, query, filter, IssuesFilterTables, wispDepsExist)
		if err != nil {
			return nil, err
		}
		return finishSearchIssuesWithCounts(out, filter)
	}

	out, err := runFilterSearchQueryInTx(ctx, tx, query, filter, IssuesFilterTables, wispDepsExist)
	if err != nil {
		return nil, err
	}

	// Skip wisps merge entirely when caller opts out (Q2: perf escape hatch).
	if filter.SkipWisps {
		return finishSearchIssuesWithCounts(out, filter)
	}

	empty, probeErr := wispsTableEmptyOrMissingInTx(ctx, tx)
	if probeErr != nil {
		return nil, fmt.Errorf("search issues with counts: wisp probe: %w", probeErr)
	}
	if empty {
		return finishSearchIssuesWithCounts(out, filter)
	}
	if !wispDepsExist {
		return finishSearchIssuesWithCounts(out, filter)
	}

	wisps, err := runFilterSearchQueryInTx(ctx, tx, query, filter, WispsFilterTables, true)
	if err != nil {
		if missingOptionalWispTable(err) {
			return finishSearchIssuesWithCounts(out, filter)
		}
		return nil, err
	}
	if len(wisps) == 0 {
		return finishSearchIssuesWithCounts(out, filter)
	}

	// Prefer the canonical wisp record when an ID exists in both tables (be-iabdi).
	wispByID := make(map[string]struct{}, len(wisps))
	for _, w := range wisps {
		if w != nil && w.Issue != nil {
			wispByID[w.Issue.ID] = struct{}{}
		}
	}
	var kept []*types.IssueWithCounts
	for _, iwc := range out {
		if iwc == nil || iwc.Issue == nil {
			kept = append(kept, iwc)
			continue
		}
		if _, dup := wispByID[iwc.Issue.ID]; !dup {
			kept = append(kept, iwc)
		}
	}
	kept = append(kept, wisps...)
	return finishSearchIssuesWithCounts(kept, filter)
}

// hydrationFor reads the two hydration opt-outs off a search filter. It is one
// function rather than two field reads at each call site so a path cannot pick
// up one of the pair and quietly drop the other.
func hydrationFor(filter types.IssueFilter) sqlbuild.CountsHydration {
	return sqlbuild.CountsHydration{SkipLabels: filter.SkipLabels, SkipCounts: filter.SkipCounts, Lite: filter.Lite}
}

func runFilterSearchQueryInTx(ctx context.Context, tx *sql.Tx, query string, filter types.IssueFilter, tables FilterTables, includeWispReverseDeps bool) ([]*types.IssueWithCounts, error) {
	whereClauses, args, err := BuildIssueFilterClauses(query, filter, tables)
	if err != nil {
		return nil, err
	}
	whereSQL := ""
	if len(whereClauses) > 0 {
		whereSQL = "WHERE " + joinAnd(whereClauses)
	}
	// A PAGE BOUND IS ONLY EVER PUSHED UNDER AN ORDER THE QUERY CAN EXPRESS —
	// the same rule searchTableInTxT applies on the plain seam. sqlbuild.OrderBy
	// renders no ORDER BY for a Go-side sort key ("id"), and a LIMIT with no
	// ORDER BY returns n rows, not the first n; this is the seam
	// bd query '<expr>' --sort id reaches on the store-shaped backends
	// (storequerier → SearchIssuesWithCounts), where BuildQueryPlan always
	// pushes a bound. So under a Go-side sort the query scans the complete
	// matching set and the same eff bound is applied below, after the order
	// exists — leaving every downstream count (the merge, the terminal
	// finishSearchIssuesWithCounts trim-then-cap) exactly what the SQL LIMIT
	// used to hand it.
	goSideSort := sqlbuild.IsGoSideSort(filter.SortBy)
	eff := EffectiveSearchLimit(filter.Limit, filter.MaxRows)
	limitSQL := ""
	if eff > 0 && !goSideSort {
		limitSQL = fmt.Sprintf("LIMIT %d", eff)
	}
	orderBy := sqlbuild.OrderBy(filter.SortBy, filter.SortDesc, "i")
	out, err := runSearchQueryInTx(ctx, tx, tables, whereSQL, orderBy, limitSQL, args, includeWispReverseDeps, hydrationFor(filter))
	if err != nil {
		return nil, err
	}
	if goSideSort {
		// scanCountsRowsInTx drops nil-Issue rows, so the accessor is safe.
		out = goSideSortAndTrim(out, func(iwc *types.IssueWithCounts) string { return iwc.Issue.ID }, filter.SortDesc, eff)
	}
	return out, nil
}

// CountsByIDsMaxRows is the largest id page a counts search hydrates by id,
// here (runSearchQueryInTx) and in domain/db's runSearchQuery.
//
// The predicate-form mega-query costs about the same whatever it returns,
// because every aggregate subquery reads its WHOLE side table: on the town's
// copy (71k issues, 108k dependencies, vn-ws7tu8m) it took 25 to 27 s for 9
// in-progress rows and for 641 open ones alike. The by-IDs form costs about
// 1.5 s per batch of sqlbuild.QueryBatchSize ids. So by id is the cheaper read
// up to roughly fifteen batches, and the mega-query is the cheaper read past
// them, as for a list of every closed bead. Ten batches keeps the switch on
// the safe side of that line.
const CountsByIDsMaxRows = 10 * sqlbuild.QueryBatchSize

// runSearchQueryInTx answers a counts search: the rows whereSQL, orderBySQL
// and limitSQL select, each with its labels, cardinalities, parent and deps.
//
// IT READS THE IDS FIRST. One indexed "SELECT i.id ... WHERE ... ORDER BY ...
// LIMIT" finds the rows, and the counts are then hydrated for exactly those
// ids (sqlbuild.SearchCountsSQL's by-IDs form), in batches, with the order put
// back in Go. The predicate form it replaces aggregated all of labels,
// dependencies and comments for every list, so a list of 9 rows paid for the
// whole database: on the live server it was 8 of the 10 queries still running
// past 5 s after vn-cdrw5n3 (vn-ws7tu8m).
//
// The answer is the same either way. The id query has the mega-query's driver,
// WHERE, ORDER BY and LIMIT, and the aggregates hang off LEFT JOINs that keep
// every driver row, so both select the same rows in the same order (every
// order ends in a unique id tiebreak, and a Go-side sort has no ORDER BY in
// either). Each count is a function of the dependency graph restricted to its
// own issue, so constraining the aggregates to the page cannot move one.
//
// It falls back to the mega-query when there is nothing to narrow (no WHERE
// and no LIMIT: every row is wanted) or the page is wider than
// CountsByIDsMaxRows. The fallback re-runs the filter, which costs one more
// indexed read and only happens on the wide lists where the mega-query wins.
//
//nolint:gosec // G201: SQL fragments are caller-built from hardcoded shapes
func runSearchQueryInTx(ctx context.Context, tx *sql.Tx, tables FilterTables, whereSQL, orderBySQL, limitSQL string, args []interface{}, includeWispReverseDeps bool, hyd sqlbuild.CountsHydration) ([]*types.IssueWithCounts, error) {
	if whereSQL == "" && limitSQL == "" {
		return runCountsMegaQueryInTx(ctx, tx, tables, whereSQL, orderBySQL, limitSQL, args, includeWispReverseDeps, hyd)
	}

	idQuery := fmt.Sprintf("SELECT i.id FROM %s i %s %s %s", tables.Main, whereSQL, orderBySQL, limitSQL)
	ids, err := queryIDsInTx(ctx, tx, idQuery, args)
	if err != nil {
		return nil, fmt.Errorf("search count %s: id page: %w", tables.Main, err)
	}
	if len(ids) == 0 {
		return nil, nil
	}
	if len(ids) > CountsByIDsMaxRows {
		return runCountsMegaQueryInTx(ctx, tx, tables, whereSQL, orderBySQL, limitSQL, args, includeWispReverseDeps, hyd)
	}
	return hydrateCountsByIDsInTx(ctx, tx, tables, ids, includeWispReverseDeps, hyd)
}

// runCountsMegaQueryInTx runs the predicate form of the counts mega-query.
// Only runSearchQueryInTx calls it, for the two cases it explains.
//
//nolint:gosec // G201: SQL fragments are caller-built from hardcoded shapes
func runCountsMegaQueryInTx(ctx context.Context, tx *sql.Tx, tables FilterTables, whereSQL, orderBySQL, limitSQL string, args []interface{}, includeWispReverseDeps bool, hyd sqlbuild.CountsHydration) ([]*types.IssueWithCounts, error) {
	searchSQL, _ := sqlbuild.SearchCountsSQL(tables, nil, whereSQL, orderBySQL, limitSQL, includeWispReverseDeps, hyd)
	return scanCountsRowsInTx(ctx, tx, tables.Main, searchSQL, args, hyd)
}

// hydrateCountsByIDsInTx hydrates the counts rows for ids, in the order of ids.
//
// The ids are chunked into sqlbuild.QueryBatchSize batches so a wide page stays
// within every backend's per-statement placeholder limit (the by-IDs form binds
// each id up to twelve times). The ids are distinct, so the batches need no
// dedupe across them. Both reads run in one transaction, so every id has its
// row; one that somehow does not is skipped rather than returned empty.
func hydrateCountsByIDsInTx(ctx context.Context, tx *sql.Tx, tables FilterTables, ids []string, includeWispReverseDeps bool, hyd sqlbuild.CountsHydration) ([]*types.IssueWithCounts, error) {
	byID := make(map[string]*types.IssueWithCounts, len(ids))
	for start := 0; start < len(ids); start += sqlbuild.QueryBatchSize {
		end := start + sqlbuild.QueryBatchSize
		if end > len(ids) {
			end = len(ids)
		}
		countsSQL, idArgs := sqlbuild.SearchCountsSQL(tables, ids[start:end], "", "", "", includeWispReverseDeps, hyd)
		rows, err := scanCountsRowsInTx(ctx, tx, tables.Main, countsSQL, idArgs, hyd)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			if r != nil && r.Issue != nil {
				byID[r.Issue.ID] = r
			}
		}
	}

	ordered := make([]*types.IssueWithCounts, 0, len(ids))
	for _, id := range ids {
		if r, ok := byID[id]; ok {
			ordered = append(ordered, r)
		}
	}
	return ordered, nil
}

// queryIDsInTx runs a one-column id query and returns the ids in row order.
//
//nolint:gosec // G201: query is builder-produced; user input rides ? placeholders.
func queryIDsInTx(ctx context.Context, tx *sql.Tx, query string, args []interface{}) ([]string, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// scanCountsRowsInTx runs a prebuilt counts mega-query and hydrates each row
// through ScanReadyWorkRowWithCounts, deduping by issue ID. It is the single
// scan/dedupe loop shared by the predicate-form search path and the by-IDs
// ready-counts path.
//
//nolint:gosec // G201: query is builder-produced; user input rides ? placeholders.
func scanCountsRowsInTx(ctx context.Context, tx *sql.Tx, mainTable, query string, args []interface{}, hyd sqlbuild.CountsHydration) ([]*types.IssueWithCounts, error) {
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("search count %s: %w", mainTable, err)
	}
	defer func() { _ = rows.Close() }()

	var out []*types.IssueWithCounts
	seen := make(map[string]bool)
	for rows.Next() {
		iwc, scanErr := ScanReadyWorkRowWithCounts(rows, hyd)
		if scanErr != nil {
			return nil, scanErr
		}
		if iwc == nil || iwc.Issue == nil {
			continue
		}
		if seen[iwc.Issue.ID] {
			continue
		}
		seen[iwc.Issue.ID] = true
		out = append(out, iwc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("search count %s: rows: %w", mainTable, err)
	}
	return out, nil
}

// finishSearchIssuesWithCounts is the single terminal hook every
// SearchIssuesWithCountsInTx exit path routes through: it sorts the merged
// result, applies the caller-facing Limit trim, and only then enforces the
// defensive MaxRows cap (be-x42v) on the delivered count — mirroring
// searchInTx's trimToSearchLimit-before-EnforceMaxRowsCap ordering and
// finishReadyWorkWithCounts in ready_work_counts.go.
//
// Trim-before-cap matters for the merged (issues+wisps) case:
// runFilterSearchQueryInTx sizes each leg's SQL LIMIT independently via
// EffectiveSearchLimit(filter.Limit, filter.MaxRows), so the merged
// pre-trim slice can hold up to ~2x that per-leg bound — e.g. Limit=2,
// MaxRows=5, 3 rows in each table merges to 6, which would trip MaxRows
// even though the page actually handed back to the caller (trimmed to
// Limit=2) is well within the cap. Checking the cap against the delivered
// count instead avoids that false positive.
//
// This does not weaken cap enforcement for a single-source result: a lone
// query's LIMIT is already bounded to at most max(Limit, MaxRows+1), so its
// result never exceeds Limit when Limit>0 and the trim is a no-op there —
// only the two-source merge can produce more rows than Limit pre-trim, and
// a genuine overage (Limit=0, or Limit>MaxRows overage that survives the
// trim) still fires.
func finishSearchIssuesWithCounts(items []*types.IssueWithCounts, filter types.IssueFilter) ([]*types.IssueWithCounts, error) {
	sortSearchIssuesWithCounts(items, filter.SortBy, filter.SortDesc)
	if filter.Limit > 0 && len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	if err := EnforceMaxRowsCap(len(items), filter.MaxRows, filter.MaxRowsSource); err != nil {
		return nil, err
	}
	return items, nil
}

// sortSearchIssuesWithCounts must order the merged issues+wisps rows the same
// way sqlbuild.OrderBy orders each per-table query; otherwise the limit cut in
// finishSearchIssuesWithCounts keeps a different row set than SQL selected.
func sortSearchIssuesWithCounts(items []*types.IssueWithCounts, sortBy string, sortDesc bool) {
	if len(items) <= 1 {
		return
	}
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if a == nil || a.Issue == nil {
			return false
		}
		if b == nil || b.Issue == nil {
			return true
		}
		return sqlbuild.Less(a.Issue, b.Issue, sortBy, sortDesc)
	})
}

func joinAnd(clauses []string) string {
	switch len(clauses) {
	case 0:
		return ""
	case 1:
		return clauses[0]
	}
	total := 0
	for _, c := range clauses {
		total += len(c)
	}
	total += 5 * (len(clauses) - 1)
	buf := make([]byte, 0, total)
	for i, c := range clauses {
		if i > 0 {
			buf = append(buf, " AND "...)
		}
		buf = append(buf, c...)
	}
	return string(buf)
}
