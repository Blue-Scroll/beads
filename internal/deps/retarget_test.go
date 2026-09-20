package deps

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// fakeStore is a dependency graph in a map, plus a log of every write in the
// order it happened. The order is half the point of this package, so the test
// has to be able to read it.
type fakeStore struct {
	// dependents maps an issue id to the edges pointing AT it.
	dependents map[string][]*types.IssueWithDependencyMetadata
	// rows maps an issue id to the raw edge rows pointing at it.
	rows map[string][]*types.Dependency

	calls   []string
	addErr  error
	dropErr error
	// failAddFor breaks the add for one dependent only, so a test can stop a
	// run halfway through.
	failAddFor string
}

func (f *fakeStore) GetDependentsWithMetadata(_ context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error) {
	return f.dependents[issueID], nil
}

func (f *fakeStore) GetDependentRecords(_ context.Context, targetID string, _ string, _ int, after string) ([]*types.Dependency, error) {
	if after != "" {
		return nil, nil
	}
	return f.rows[targetID], nil
}

func (f *fakeStore) AddDependencyWithOptions(_ context.Context, dep *types.Dependency, _ string, opts storage.DependencyAddOptions) error {
	if f.addErr != nil {
		return f.addErr
	}
	if f.failAddFor != "" && dep.IssueID == f.failAddFor {
		return errors.New("refused")
	}
	f.calls = append(f.calls, "add "+dep.IssueID+" -> "+dep.DependsOnID+" ("+string(dep.Type)+")")
	if !opts.EmitEvent {
		f.calls = append(f.calls, "add-without-history")
	}
	if dep.Metadata != "" {
		f.calls = append(f.calls, "add-metadata "+dep.Metadata)
	}
	return nil
}

func (f *fakeStore) RemoveDependencyWithOptions(_ context.Context, issueID, dependsOnID string, _ string, _ storage.DependencyRemoveOptions) error {
	if f.dropErr != nil {
		return f.dropErr
	}
	f.calls = append(f.calls, "remove "+issueID+" -> "+dependsOnID)
	return nil
}

func dep(id string, status types.Status, t types.DependencyType) *types.IssueWithDependencyMetadata {
	return &types.IssueWithDependencyMetadata{
		Issue:          types.Issue{ID: id, Status: status},
		DependencyType: t,
	}
}

func open(id string) *types.Issue { return &types.Issue{ID: id, Status: types.StatusOpen} }
func shut(id string) *types.Issue { return &types.Issue{ID: id, Status: types.StatusClosed} }
func log(f *fakeStore) string     { return strings.Join(f.calls, "; ") }
func kinds(m []Move) string {
	parts := make([]string, 0, len(m))
	for _, mv := range m {
		parts = append(parts, mv.Dependent+":"+string(mv.Kind))
	}
	return strings.Join(parts, "; ")
}

// A blocking fence moves, and the new edge is added BEFORE the old one comes
// off. The other order leaves a window with no fence at all.
func TestRetargetAddsBeforeItRemoves(t *testing.T) {
	f := &fakeStore{dependents: map[string][]*types.IssueWithDependencyMetadata{
		"dying": {dep("a", types.StatusOpen, types.DepBlocks)},
	}}

	moves, err := Retarget(context.Background(), f, "dying", open("surv"), "tester")
	if err != nil {
		t.Fatalf("Retarget: %v", err)
	}
	if got, want := log(f), "add a -> surv (blocks); remove a -> dying"; got != want {
		t.Errorf("call order:\n got %q\nwant %q", got, want)
	}
	if got, want := kinds(moves), "a:repointed"; got != want {
		t.Errorf("moves: got %q, want %q", got, want)
	}
}

// Only the three blocking edge types are fences. parent-child is a hierarchy,
// and related/discovered-from hold nothing back at all.
func TestRetargetMovesOnlyBlockingEdges(t *testing.T) {
	f := &fakeStore{dependents: map[string][]*types.IssueWithDependencyMetadata{
		"dying": {
			dep("blocks", types.StatusOpen, types.DepBlocks),
			dep("cond", types.StatusOpen, types.DepConditionalBlocks),
			dep("waits", types.StatusOpen, types.DepWaitsFor),
			dep("child", types.StatusOpen, types.DepParentChild),
			dep("related", types.StatusOpen, types.DepRelated),
			dep("found", types.StatusOpen, types.DepDiscoveredFrom),
		},
	}}

	moves, err := Retarget(context.Background(), f, "dying", open("surv"), "tester")
	if err != nil {
		t.Fatalf("Retarget: %v", err)
	}
	if got, want := kinds(moves), "blocks:repointed; cond:repointed; waits:repointed"; got != want {
		t.Errorf("moves: got %q, want %q", got, want)
	}
	for _, untouched := range []string{"child", "related", "found"} {
		if strings.Contains(log(f), untouched) {
			t.Errorf("%s should not have been touched: %s", untouched, log(f))
		}
	}
}

// A closed dependent is history. Rewriting its edges changes the record of
// what really blocked what, and it is not going back in a work pool.
func TestRetargetLeavesClosedDependents(t *testing.T) {
	f := &fakeStore{dependents: map[string][]*types.IssueWithDependencyMetadata{
		"dying": {
			dep("done", types.StatusClosed, types.DepBlocks),
			dep("live", types.StatusOpen, types.DepBlocks),
		},
	}}

	moves, err := Retarget(context.Background(), f, "dying", open("surv"), "tester")
	if err != nil {
		t.Fatalf("Retarget: %v", err)
	}
	if got, want := kinds(moves), "live:repointed"; got != want {
		t.Errorf("moves: got %q, want %q", got, want)
	}
	if strings.Contains(log(f), "done") {
		t.Errorf("a closed dependent was rewritten: %s", log(f))
	}
}

// The survivor was itself held behind the dying issue. Nothing can depend on
// itself, so the dead edge is dropped and no new one takes its place.
func TestRetargetDropsTheSurvivorsOwnEdge(t *testing.T) {
	f := &fakeStore{dependents: map[string][]*types.IssueWithDependencyMetadata{
		"dying": {dep("surv", types.StatusOpen, types.DepBlocks)},
	}}

	moves, err := Retarget(context.Background(), f, "dying", open("surv"), "tester")
	if err != nil {
		t.Fatalf("Retarget: %v", err)
	}
	if got, want := log(f), "remove surv -> dying"; got != want {
		t.Errorf("calls: got %q, want %q", got, want)
	}
	if got, want := kinds(moves), "surv:dropped-self"; got != want {
		t.Errorf("moves: got %q, want %q", got, want)
	}
}

// The survivor already holds this issue back, either by a blocking edge or by
// being its parent. Adding a second edge is noise, and on the parent-child
// case `bd dep add` refuses it outright.
func TestRetargetSkipsTheAddWhenTheSurvivorAlreadyHolds(t *testing.T) {
	for _, tc := range []struct {
		name string
		edge types.DependencyType
	}{
		{"blocking edge", types.DepBlocks},
		{"child of the survivor", types.DepParentChild},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeStore{dependents: map[string][]*types.IssueWithDependencyMetadata{
				"dying": {dep("a", types.StatusOpen, types.DepBlocks)},
				"surv":  {dep("a", types.StatusOpen, tc.edge)},
			}}

			moves, err := Retarget(context.Background(), f, "dying", open("surv"), "tester")
			if err != nil {
				t.Fatalf("Retarget: %v", err)
			}
			if got, want := log(f), "remove a -> dying"; got != want {
				t.Errorf("calls: got %q, want %q", got, want)
			}
			if got, want := kinds(moves), "a:already-held"; got != want {
				t.Errorf("moves: got %q, want %q", got, want)
			}
		})
	}
}

// A waits-for edge keeps its gate in the row's metadata, and a stored
// waits-for row is never allowed to have empty metadata. So the payload has to
// ride along, or the move writes a malformed gate.
func TestRetargetCarriesTheEdgePayload(t *testing.T) {
	f := &fakeStore{
		dependents: map[string][]*types.IssueWithDependencyMetadata{
			"dying": {dep("a", types.StatusOpen, types.DepWaitsFor)},
		},
		rows: map[string][]*types.Dependency{
			"dying": {{
				ID:          "row-1",
				IssueID:     "a",
				DependsOnID: "dying",
				Type:        types.DepWaitsFor,
				Metadata:    `{"gate":"any-children"}`,
			}},
		},
	}

	if _, err := Retarget(context.Background(), f, "dying", open("surv"), "tester"); err != nil {
		t.Fatalf("Retarget: %v", err)
	}
	if !strings.Contains(log(f), `add-metadata {"gate":"any-children"}`) {
		t.Errorf("the gate did not ride along: %s", log(f))
	}
}

// Every write records history. A fence moving is exactly the thing somebody
// will come looking for later.
func TestRetargetRecordsHistory(t *testing.T) {
	f := &fakeStore{dependents: map[string][]*types.IssueWithDependencyMetadata{
		"dying": {dep("a", types.StatusOpen, types.DepBlocks)},
	}}

	if _, err := Retarget(context.Background(), f, "dying", open("surv"), "tester"); err != nil {
		t.Fatalf("Retarget: %v", err)
	}
	if !strings.Contains(log(f), "add a -> surv") {
		t.Fatalf("the edge was never added, so there is no history to check: %s", log(f))
	}
	if strings.Contains(log(f), "add-without-history") {
		t.Errorf("the move was written without a history event: %s", log(f))
	}
}

// The refusal that is the point. A closed survivor cannot hold anything back,
// so moving the fence there releases the dependents just as surely as dropping
// it. Nothing is written.
func TestRetargetRefusesAClosedSurvivor(t *testing.T) {
	f := &fakeStore{dependents: map[string][]*types.IssueWithDependencyMetadata{
		"dying": {dep("a", types.StatusOpen, types.DepBlocks)},
	}}

	_, err := Retarget(context.Background(), f, "dying", shut("surv"), "tester")
	var closedErr *ClosedSurvivorError
	if !errors.As(err, &closedErr) {
		t.Fatalf("want a ClosedSurvivorError, got %v", err)
	}
	if len(f.calls) != 0 {
		t.Errorf("the refusal still wrote something: %s", log(f))
	}
	if !strings.Contains(err.Error(), "a") || !strings.Contains(err.Error(), "surv") {
		t.Errorf("the message should name the held issue and the survivor: %s", err)
	}
}

// A closed survivor with nothing held behind the dying issue is harmless:
// marking an old issue as a duplicate of another old issue loses nothing.
func TestRetargetAllowsAClosedSurvivorWhenNothingIsHeld(t *testing.T) {
	f := &fakeStore{dependents: map[string][]*types.IssueWithDependencyMetadata{
		"dying": {dep("done", types.StatusClosed, types.DepBlocks)},
	}}

	moves, err := Retarget(context.Background(), f, "dying", shut("surv"), "tester")
	if err != nil {
		t.Fatalf("Retarget: %v", err)
	}
	if len(moves) != 0 || len(f.calls) != 0 {
		t.Errorf("nothing should have happened: moves=%v calls=%s", moves, log(f))
	}
}

// If the new fence cannot go up, the old one stays. A stuck issue is
// recoverable. A released one is somebody picking up work nobody cleared.
func TestRetargetKeepsTheOldFenceWhenTheAddFails(t *testing.T) {
	f := &fakeStore{
		dependents: map[string][]*types.IssueWithDependencyMetadata{
			"dying": {dep("a", types.StatusOpen, types.DepBlocks)},
		},
		addErr: errors.New("cycle"),
	}

	moves, err := Retarget(context.Background(), f, "dying", open("surv"), "tester")
	if err == nil {
		t.Fatal("want an error")
	}
	if len(f.calls) != 0 {
		t.Errorf("the old fence was touched anyway: %s", log(f))
	}
	if len(moves) != 0 {
		t.Errorf("no move happened, so none should be reported: %v", moves)
	}
	if !strings.Contains(err.Error(), "still holds it back") {
		t.Errorf("the message should say the fence still stands: %s", err)
	}
}

// The new fence is up and the old one will not come off. That is safe, and the
// message has to say so, or somebody will "fix" it by removing the new one.
func TestRetargetSaysBothFencesStandWhenTheRemoveFails(t *testing.T) {
	f := &fakeStore{
		dependents: map[string][]*types.IssueWithDependencyMetadata{
			"dying": {dep("a", types.StatusOpen, types.DepBlocks)},
		},
		dropErr: errors.New("gone"),
	}

	_, err := Retarget(context.Background(), f, "dying", open("surv"), "tester")
	if err == nil {
		t.Fatal("want an error")
	}
	if !strings.Contains(err.Error(), "which is safe") {
		t.Errorf("the message should say the issue is still held: %s", err)
	}
}

// Nothing held back means no writes at all.
func TestRetargetWritesNothingWhenNothingIsHeld(t *testing.T) {
	f := &fakeStore{}

	moves, err := Retarget(context.Background(), f, "dying", open("surv"), "tester")
	if err != nil {
		t.Fatalf("Retarget: %v", err)
	}
	if len(moves) != 0 || len(f.calls) != 0 {
		t.Errorf("nothing should have happened: moves=%v calls=%s", moves, log(f))
	}
}

// A run that stops halfway still reports what it already moved, so whoever
// reads the error knows which fences are where.
func TestRetargetReportsThePartialRun(t *testing.T) {
	f := &fakeStore{
		dependents: map[string][]*types.IssueWithDependencyMetadata{
			"dying": {
				dep("first", types.StatusOpen, types.DepBlocks),
				dep("second", types.StatusOpen, types.DepBlocks),
			},
		},
		failAddFor: "second",
	}

	moves, err := Retarget(context.Background(), f, "dying", open("surv"), "tester")
	if err == nil {
		t.Fatal("want an error")
	}
	if got, want := kinds(moves), "first:repointed"; got != want {
		t.Errorf("the finished move should still be reported: got %q, want %q", got, want)
	}
	if got, want := log(f), "add first -> surv (blocks); remove first -> dying"; got != want {
		t.Errorf("calls: got %q, want %q", got, want)
	}
}
