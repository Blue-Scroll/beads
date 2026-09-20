// Package deps holds rules about the dependency graph that more than one
// command has to agree on.
//
// It starts with one rule, learned from one bug. `bd duplicate` and
// `bd supersede` close the dying issue. Every issue that was held back behind
// that issue is released the moment it closes, and a work pool can hand a
// released issue out within minutes. Nothing goes red, because from the
// outside a released issue looks exactly like an issue that was never blocked.
//
// So the fence has to move to the surviving issue BEFORE the dying one closes,
// and it has to move one edge at a time, new edge first. Then the hold never
// lifts, not even for a moment.
package deps

import (
	"context"
	"fmt"
	"strings"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// recordPage bounds one raw-edge read. GetDependentRecords caps what it
// returns, so the rows are read a page at a time and keyset on the row id.
const recordPage = 500

// RetargetStore is the slice of the store that moving a fence needs. It is
// narrow on purpose: a test can fill it in twenty lines, and a caller cannot
// reach through it for something that is not part of moving a fence.
type RetargetStore interface {
	GetDependentsWithMetadata(ctx context.Context, issueID string) ([]*types.IssueWithDependencyMetadata, error)
	GetDependentRecords(ctx context.Context, targetID string, depType string, limit int, afterID string) ([]*types.Dependency, error)
	AddDependencyWithOptions(ctx context.Context, dep *types.Dependency, actor string, opts storage.DependencyAddOptions) error
	RemoveDependencyWithOptions(ctx context.Context, issueID, dependsOnID string, actor string, opts storage.DependencyRemoveOptions) error
}

// Fence is one edge that holds an open issue back.
type Fence struct {
	// Dependent is the issue being held back.
	Dependent string
	// Type is the edge that holds it: blocks, conditional-blocks or waits-for.
	Type types.DependencyType
}

// MoveKind says what happened to one fence.
type MoveKind string

const (
	// MoveRepointed means the fence now rests on the survivor instead.
	MoveRepointed MoveKind = "repointed"
	// MoveDroppedSelf means the dependent WAS the survivor. Nothing can depend
	// on itself, so the dead edge was dropped and no new one took its place.
	MoveDroppedSelf MoveKind = "dropped-self"
	// MoveAlreadyHeld means the survivor already held this issue back, so the
	// dead edge was dropped and no second edge was added.
	MoveAlreadyHeld MoveKind = "already-held"
)

// Move is one fence this package acted on.
type Move struct {
	Dependent string               `json:"dependent"`
	Type      types.DependencyType `json:"type"`
	Kind      MoveKind             `json:"kind"`
}

// ClosedSurvivorError is the refusal that is the point of this package. A
// closed issue holds nothing back, so hanging a fence on one releases the
// dependents just as surely as dropping the fence does.
type ClosedSurvivorError struct {
	Dying    string
	Survivor string
	Fences   []Fence
}

func (e *ClosedSurvivorError) Error() string {
	held := make([]string, 0, len(e.Fences))
	for _, f := range e.Fences {
		held = append(held, f.Dependent)
	}
	return fmt.Sprintf(
		"%s still holds back %d open issue(s) (%s), and %s is already closed. "+
			"A closed issue holds nothing back, so moving those fences onto it would release them. "+
			"Re-open %s, or move the edges yourself with `bd dep`, then run this again",
		e.Dying, len(e.Fences), strings.Join(held, ", "), e.Survivor, e.Survivor)
}

// OpenFences returns the fences that hold an OPEN issue behind issueID.
//
// Two filters, and each one is a decision:
//
//   - Only blocking edges count. parent-child is a hierarchy, not a fence, and
//     re-parenting somebody's tree is a judgement call, not a repair.
//   - Only open dependents count. A closed issue's edges are the record of what
//     really blocked what, and it is not going back in a work pool.
func OpenFences(ctx context.Context, s RetargetStore, issueID string) ([]Fence, error) {
	dependents, err := s.GetDependentsWithMetadata(ctx, issueID)
	if err != nil {
		return nil, fmt.Errorf("reading what %s holds back: %w", issueID, err)
	}
	fences := make([]Fence, 0, len(dependents))
	for _, d := range dependents {
		if d == nil || !d.DependencyType.IsBlockingEdge() || d.Status == types.StatusClosed {
			continue
		}
		fences = append(fences, Fence{Dependent: d.ID, Type: d.DependencyType})
	}
	return fences, nil
}

// Retarget moves every fence that holds an open issue behind dyingID so that
// it holds that issue behind the survivor instead. Call it BEFORE closing the
// dying issue.
//
// It takes the survivor as a whole issue, not an id, so the closed-survivor
// refusal lives here and no caller can forget it.
//
// It returns what it moved, in the order it moved it. On an error it returns
// the moves that already happened, so the caller can print them: a half-done
// move is still worth reading.
func Retarget(ctx context.Context, s RetargetStore, dyingID string, survivor *types.Issue, actor string) ([]Move, error) {
	if survivor == nil {
		return nil, fmt.Errorf("no survivor issue to move the fences of %s onto", dyingID)
	}

	fences, err := OpenFences(ctx, s, dyingID)
	if err != nil {
		return nil, err
	}
	if len(fences) == 0 {
		// Nothing is held back, so there is nothing to lose. This is also why a
		// closed survivor is fine here: the refusal below protects dependents,
		// and there are none.
		return nil, nil
	}
	if survivor.Status == types.StatusClosed {
		return nil, &ClosedSurvivorError{Dying: dyingID, Survivor: survivor.ID, Fences: fences}
	}

	held, err := alreadyHeldBy(ctx, s, survivor.ID)
	if err != nil {
		return nil, err
	}
	rows, err := inboundEdges(ctx, s, dyingID)
	if err != nil {
		return nil, err
	}

	moves := make([]Move, 0, len(fences))
	for _, f := range fences {
		drop := func() error {
			return s.RemoveDependencyWithOptions(ctx, f.Dependent, dyingID, actor,
				storage.DependencyRemoveOptions{EmitEvent: true})
		}

		switch {
		case f.Dependent == survivor.ID:
			if err := drop(); err != nil {
				return moves, fmt.Errorf("dropping the dead %s edge %s -> %s: %w", f.Type, f.Dependent, dyingID, err)
			}
			moves = append(moves, Move{Dependent: f.Dependent, Type: f.Type, Kind: MoveDroppedSelf})

		case held[f.Dependent]:
			if err := drop(); err != nil {
				return moves, fmt.Errorf("dropping the dead %s edge %s -> %s: %w", f.Type, f.Dependent, dyingID, err)
			}
			moves = append(moves, Move{Dependent: f.Dependent, Type: f.Type, Kind: MoveAlreadyHeld})

		default:
			edge := &types.Dependency{IssueID: f.Dependent, DependsOnID: survivor.ID, Type: f.Type}
			if row := rows[edgeKey(f.Dependent, f.Type)]; row != nil {
				// Carry the edge's payload across. A waits-for edge keeps its
				// gate in Metadata, and a stored waits-for row is never allowed
				// to have empty metadata, so a copy that dropped it would write
				// a malformed gate. The payload is copied byte for byte and
				// never re-serialized, so an unknown field cannot be lost.
				edge.Metadata = row.Metadata
				edge.ThreadID = row.ThreadID
			}

			// ADD FIRST, REMOVE SECOND. In between, the dependent is held
			// behind BOTH issues, which holds. The other order leaves a window
			// where it is held behind neither, and a work pool sweep fits in
			// that window.
			if err := s.AddDependencyWithOptions(ctx, edge, actor, storage.DependencyAddOptions{EmitEvent: true}); err != nil {
				return moves, fmt.Errorf(
					"could not fence %s behind %s with a %s edge: %w. Nothing was removed, so %s still holds it back. Fix that, then run this again",
					f.Dependent, survivor.ID, f.Type, err, dyingID)
			}
			if err := drop(); err != nil {
				return moves, fmt.Errorf(
					"%s is now fenced behind both %s and %s, which is safe, but the old %s edge would not come off: %w. Remove it by hand",
					f.Dependent, survivor.ID, dyingID, f.Type, err)
			}
			moves = append(moves, Move{Dependent: f.Dependent, Type: f.Type, Kind: MoveRepointed})
		}
	}
	return moves, nil
}

// alreadyHeldBy answers "does the survivor already hold this issue back?" for
// every issue at once.
//
// parent-child counts here even though it is not a fence we would create: a
// child of the survivor is already held by the hierarchy, and `bd dep add`
// refuses to add a blocking edge on top of it. Counting it lets the dead edge
// come off instead of the whole run stopping.
func alreadyHeldBy(ctx context.Context, s RetargetStore, survivorID string) (map[string]bool, error) {
	dependents, err := s.GetDependentsWithMetadata(ctx, survivorID)
	if err != nil {
		return nil, fmt.Errorf("reading what %s already holds back: %w", survivorID, err)
	}
	held := make(map[string]bool, len(dependents))
	for _, d := range dependents {
		if d == nil {
			continue
		}
		if d.DependencyType.IsBlockingEdge() || d.DependencyType == types.DepParentChild {
			held[d.ID] = true
		}
	}
	return held, nil
}

// inboundEdges reads the raw edge rows pointing at targetID, keyed by
// dependent and type. The rows carry the payload (gate metadata, thread id)
// that the hydrated read does not.
func inboundEdges(ctx context.Context, s RetargetStore, targetID string) (map[string]*types.Dependency, error) {
	rows := map[string]*types.Dependency{}
	after := ""
	for {
		page, err := s.GetDependentRecords(ctx, targetID, "", recordPage, after)
		if err != nil {
			return nil, fmt.Errorf("reading the edges that point at %s: %w", targetID, err)
		}
		if len(page) == 0 {
			return rows, nil
		}
		for _, row := range page {
			if row == nil {
				continue
			}
			rows[edgeKey(row.IssueID, row.Type)] = row
		}
		last := page[len(page)-1]
		if last == nil || last.ID == "" || last.ID == after {
			// No keyset to page on. Stop rather than ask for the same page
			// forever; the rows already read are still correct.
			return rows, nil
		}
		after = last.ID
	}
}

func edgeKey(dependent string, t types.DependencyType) string {
	return dependent + "\x00" + string(t)
}
