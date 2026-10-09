package issueops

import (
	"context"
	"database/sql/driver"
	"regexp"
	"testing"

	sqlmock "github.com/DATA-DOG/go-sqlmock"

	"github.com/steveyegge/beads/internal/storage/sqlbuild"
)

// countsRowColumns is a counts mega-query row: the issue columns, then the six
// aggregate columns ScanReadyWorkRowWithCounts reads by position.
func countsRowColumns() []string {
	return append(issueColumns(), "labels_json", "dep_count", "rdep_count", "comment_count", "parent_id", "deps_json")
}

func countsRows(ids ...string) *sqlmock.Rows {
	rows := sqlmock.NewRows(countsRowColumns())
	for _, id := range ids {
		values := append(issueRowValues(id, id), []driver.Value{nil, 0, 0, 0, nil, nil}...)
		rows.AddRow(values...)
	}
	return rows
}

func idRows(ids ...string) *sqlmock.Rows {
	rows := sqlmock.NewRows([]string{"id"})
	for _, id := range ids {
		rows.AddRow(id)
	}
	return rows
}

// TestRunSearchQueryInTxReadsIDsFirst pins the read vn-ws7tu8m introduced: a
// filtered counts search asks for its ids with the cheap indexed query, then
// hydrates the counts for exactly those ids, and keeps the id query's order
// whatever order the hydrate returns rows in. The predicate-form mega-query it
// replaces aggregated whole side tables for every list, 25 s on the town's data.
//
// Neuter: make runSearchQueryInTx call runCountsMegaQueryInTx unconditionally
// and the first expectation fails, because the mega-query is not an id query.
func TestRunSearchQueryInTxReadsIDsFirst(t *testing.T) {
	t.Parallel()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock: %v", err)
	}
	defer db.Close()

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT i.id FROM issues i WHERE status = ? ORDER BY i.priority ASC LIMIT 5")).
		WithArgs("open").
		WillReturnRows(idRows("vn-b", "vn-a"))
	// The hydrate is the by-IDs form, and answers in the OTHER order.
	mock.ExpectQuery(regexp.QuoteMeta("WHERE i.id IN (?,?)")).
		WillReturnRows(countsRows("vn-a", "vn-b"))
	mock.ExpectCommit()

	tx, err := db.Begin()
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	out, err := runSearchQueryInTx(context.Background(), tx, IssuesFilterTables,
		"WHERE status = ?", "ORDER BY i.priority ASC", "LIMIT 5", []any{"open"}, false, sqlbuild.CountsHydration{})
	if err != nil {
		t.Fatalf("runSearchQueryInTx: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if len(out) != 2 || out[0].Issue.ID != "vn-b" || out[1].Issue.ID != "vn-a" {
		var got []string
		for _, r := range out {
			got = append(got, r.Issue.ID)
		}
		t.Errorf("rows = %v, want the id query's order [vn-b vn-a]", got)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Error(err)
	}
}

// TestRunSearchQueryInTxKeepsTheMegaQueryWhereItWins pins the two cases the id
// read hands back to the predicate form: a list with nothing to narrow (no
// WHERE, no LIMIT), and a page wider than CountsByIDsMaxRows, where the by-IDs
// batches would cost more than the one mega-query.
func TestRunSearchQueryInTxKeepsTheMegaQueryWhereItWins(t *testing.T) {
	t.Parallel()

	t.Run("nothing to narrow", func(t *testing.T) {
		t.Parallel()
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close()
		mock.ExpectBegin()
		// No id query first: the very first statement is the mega-query.
		mock.ExpectQuery(regexp.QuoteMeta("JSON_ARRAYAGG(label)")).WillReturnRows(countsRows("vn-a"))
		mock.ExpectCommit()

		tx, _ := db.Begin()
		out, err := runSearchQueryInTx(context.Background(), tx, IssuesFilterTables, "", "", "", nil, false, sqlbuild.CountsHydration{})
		if err != nil {
			t.Fatalf("runSearchQueryInTx: %v", err)
		}
		_ = tx.Commit()
		if len(out) != 1 {
			t.Errorf("got %d rows, want 1", len(out))
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})

	t.Run("page wider than the by-IDs limit", func(t *testing.T) {
		t.Parallel()
		db, mock, err := sqlmock.New()
		if err != nil {
			t.Fatalf("sqlmock: %v", err)
		}
		defer db.Close()
		ids := make([]string, CountsByIDsMaxRows+1)
		for i := range ids {
			ids[i] = "vn-" + string(rune('a'+i%26)) + string(rune('a'+i/26%26)) + string(rune('a'+i/676))
		}
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT i.id FROM issues i WHERE status = ?")).
			WithArgs("closed").
			WillReturnRows(idRows(ids...))
		// The fallback re-runs the filter inside the mega-query's derived driver.
		mock.ExpectQuery(regexp.QuoteMeta("SELECT i.*")).
			WithArgs("closed").
			WillReturnRows(countsRows("vn-a"))
		mock.ExpectCommit()

		tx, _ := db.Begin()
		if _, err := runSearchQueryInTx(context.Background(), tx, IssuesFilterTables, "WHERE status = ?", "", "", []any{"closed"}, false, sqlbuild.CountsHydration{}); err != nil {
			t.Fatalf("runSearchQueryInTx: %v", err)
		}
		_ = tx.Commit()
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
	})
}
