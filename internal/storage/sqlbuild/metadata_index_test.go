package sqlbuild

import (
	"reflect"
	"strings"
	"testing"
)

// TestAppendMetadataClausesIndexedKeyKeepsJSONPredicate pins the shape a
// gc.root_bead_id field match takes (vn-s54d6fy): the generated-column
// predicate the index can seek, AND the original JSON predicate, which stays
// the exact match because the column is truncated to 255 characters.
func TestAppendMetadataClausesIndexedKeyKeepsJSONPredicate(t *testing.T) {
	t.Parallel()

	where, args, err := AppendMetadataClauses(nil, nil, "", map[string]string{
		"gc.root_bead_id": "vn-root",
		"gc.kind":         "workflow",
	})
	if err != nil {
		t.Fatalf("AppendMetadataClauses: %v", err)
	}
	wantWhere := []string{
		// Keys are sorted: gc.kind before gc.root_bead_id.
		"JSON_UNQUOTE(JSON_EXTRACT(metadata, ?)) = ?",
		"gc_root_bead_id = ?",
		"JSON_UNQUOTE(JSON_EXTRACT(metadata, ?)) = ?",
	}
	wantArgs := []any{
		`$."gc.kind"`, "workflow",
		"vn-root",
		`$."gc.root_bead_id"`, "vn-root",
	}
	if !reflect.DeepEqual(where, wantWhere) {
		t.Fatalf("where = %#v\nwant   %#v", where, wantWhere)
	}
	if !reflect.DeepEqual(args, wantArgs) {
		t.Fatalf("args = %#v\nwant   %#v", args, wantArgs)
	}
}

// TestAppendMetadataClausesIndexedKeyTruncatesLikeLEFT: a value longer than
// the column binds its first 255 characters (not bytes) to the column
// predicate, exactly what LEFT(..., 255) stored, and the full value to the
// JSON predicate.
func TestAppendMetadataClausesIndexedKeyTruncatesLikeLEFT(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("é", 300) // 2 bytes each, so bytes != characters
	_, args, err := AppendMetadataClauses(nil, nil, "", map[string]string{"gc.root_bead_id": long})
	if err != nil {
		t.Fatalf("AppendMetadataClauses: %v", err)
	}
	if got := args[0].(string); got != strings.Repeat("é", indexedMetadataColumnWidth) {
		t.Fatalf("column arg has %d characters, want %d", len([]rune(got)), indexedMetadataColumnWidth)
	}
	if got := args[2].(string); got != long {
		t.Fatalf("JSON arg was cut to %d characters, want the full %d", len([]rune(got)), 300)
	}
}

// TestAppendMetadataClausesUnindexedKeyUnchanged: any other key keeps the
// single JSON predicate. Only keys whose column a migration created may name
// one.
func TestAppendMetadataClausesUnindexedKeyUnchanged(t *testing.T) {
	t.Parallel()

	where, args, err := AppendMetadataClauses(nil, nil, "gc.root_bead_id", map[string]string{"gc.routed_to": "x"})
	if err != nil {
		t.Fatalf("AppendMetadataClauses: %v", err)
	}
	wantWhere := []string{
		"JSON_EXTRACT(metadata, ?) IS NOT NULL",
		"JSON_UNQUOTE(JSON_EXTRACT(metadata, ?)) = ?",
	}
	if !reflect.DeepEqual(where, wantWhere) {
		t.Fatalf("where = %#v\nwant   %#v", where, wantWhere)
	}
	if len(args) != 3 {
		t.Fatalf("args = %#v, want 3", args)
	}
}
