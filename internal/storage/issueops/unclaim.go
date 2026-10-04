package issueops

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/steveyegge/beads/internal/storage"
	"github.com/steveyegge/beads/internal/types"
)

// statusAfterUnclaim says what status a release leaves behind, and whether a
// release is allowed from this status at all. Both unclaim paths use it, so
// they cannot disagree.
//
// A release undoes the claim and nothing else. A claim moves an issue from open
// to in_progress, so a release moves in_progress (or an assigned open issue)
// back to open.
//
// A deferred issue STAYS deferred, and its defer_until is not touched. Reopening
// it would make it ready work on the spot, and the next worker would claim it
// before its date. That is exactly what happened when this only accepted open
// and in_progress: the one order that worked was "unclaim, then defer again",
// and in the seconds between the two writes another worker's hook claimed the
// issue (vn-mpifu59). Now "defer, then unclaim" works, and the issue is never
// open in between.
//
// Every other status (closed, blocked, hooked, pinned, a custom status) is not
// the claim's doing, so a release refuses it rather than guess.
func statusAfterUnclaim(current types.Status) (types.Status, bool) {
	switch current {
	case types.StatusOpen, types.StatusInProgress:
		return types.StatusOpen, true
	case types.StatusDeferred:
		return types.StatusDeferred, true
	default:
		return "", false
	}
}

// errUnclaimStatus is the error for a status statusAfterUnclaim refuses. It
// names the status, so the caller is not left with a bare "no matching row".
func errUnclaimStatus(id string, status types.Status) error {
	if status == types.StatusClosed {
		return fmt.Errorf("cannot unclaim closed issue %s", id)
	}
	return fmt.Errorf("cannot unclaim issue %s: its status is %q, and unclaim only releases an open, in_progress or deferred issue", id, status)
}

// UnclaimIssueInTx atomically releases a claimed issue: it clears the assignee,
// sets the status statusAfterUnclaim picks (open, or deferred for a deferred
// issue), clears started_at, deletes the issue's lease row
// (see UpsertLeaseInTx) and rewrites row_lock so a concurrent reclaim or close
// on the same row conflicts rather than silently cell-merging (see the
// row_lock invariant in lease.go). Records an "unclaimed" event.
//
// Ownership: only the current assignee may release its own claim. A mismatched
// actor is rejected with storage.ErrNotOwner rather than a silent no-op, so a
// second agent cannot yank a claim it does not hold. Ownership is compared
// under actorMatches (ga-5ksp5), so two spellings of the same Gas Town
// identity (e.g. a dotted alias vs its session-name form) both count as the
// owner — see canonicalActor. Pass force=true to bypass the ownership check
// (admin/reaper use, threaded from `bd unclaim --force`).
//
// Only works on issues that have an assignee and a status statusAfterUnclaim
// accepts (open, in_progress or deferred). Returns error if:
//   - Issue has any other status, closed included (errUnclaimStatus)
//   - Issue has no assignee (nothing to unclaim)
//   - Issue is claimed by a different actor and force is false (ErrNotOwner)
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func UnclaimIssueInTx(ctx context.Context, tx DBTX, id string, actor string, force bool) error {
	// Route to the correct table (issues/wisps) automatically, matching
	// ClaimIssueInTx — a wisp claim lives in the wisp tables, so its release
	// must update them too rather than no-op against the permanent issues table.
	isWisp := IsActiveWispInTx(ctx, tx, id)
	issueTable, _, eventTable, _ := WispTableRouting(isWisp)

	oldIssue, err := GetIssueInTx(ctx, tx, id)
	if err != nil {
		return fmt.Errorf("failed to get issue for unclaim: %w", err)
	}

	// Validate: the status must be one a release can undo (not closed, not
	// blocked, and so on). See statusAfterUnclaim.
	nextStatus, ok := statusAfterUnclaim(oldIssue.Status)
	if !ok {
		return errUnclaimStatus(id, oldIssue.Status)
	}

	// Validate: must have an assignee to unclaim
	if oldIssue.Assignee == "" {
		return fmt.Errorf("issue %s is not assigned", id)
	}

	// Validate ownership unless the caller forced the release. Without force, a
	// process may only release its own claim. Compared under actorMatches, not
	// verbatim, so a caller naming its own identity under a different layer's
	// spelling (ga-5ksp5) is not refused as a stranger.
	if !force && !actorMatches(oldIssue.Assignee, actor) {
		return fmt.Errorf("%w: %s is held by %s; coordinate with the holder — pass --force only if their claim is abandoned (crashed agent, expired lease)",
			storage.ErrNotOwner, id, oldIssue.Assignee)
	}

	now := time.Now().UTC()

	// Atomic UPDATE: clear assignee, set the status statusAfterUnclaim picked,
	// clear started_at, and rewrite row_lock. The predicate CASes on row_lock
	// rather than assignee (ga-5ksp5): ownership was already authorized above
	// (or bypassed by force) against the row read into oldIssue, and row_lock
	// is rewritten by every path that mutates status/assignee/started_at (see
	// the freshRowLock invariant in lease.go) — so requiring it to still equal
	// oldIssue.RowVersion detects a claim that changed hands (or was released,
	// or closed) between that read and this write exactly as precisely as the
	// old `assignee = <actor>` predicate did, without embedding a
	// spelling-sensitive string comparison in SQL. force does not exempt this
	// check: force only widens WHO may unclaim, not whether the row is still
	// the one we read. The status is pinned to the one read too, so a deferred
	// issue is released as deferred and never reopened (vn-mpifu59), and a row
	// whose status moved is named by errUnclaimRowMoved below.
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s
		SET assignee = '', status = ?, updated_at = ?,
		    started_at = NULL, row_lock = ?
		WHERE id = ? AND status = ? AND row_lock = ?
	`, issueTable), nextStatus, now, freshRowLock(), id, oldIssue.Status, oldIssue.RowVersion)
	if err != nil {
		return fmt.Errorf("failed to unclaim issue: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		// The pre-checks passed, so a 0-row result means the row changed
		// underneath us: re-read to disambiguate an ownership change from a
		// status change. actorMatches, not verbatim, mirrors the precheck above.
		current, gerr := GetIssueInTx(ctx, tx, id)
		if gerr != nil {
			return fmt.Errorf("failed to unclaim issue %s: no matching row", id)
		}
		if !force && !actorMatches(current.Assignee, actor) {
			return fmt.Errorf("%w: %s is held by %s; coordinate with the holder — pass --force only if their claim is abandoned (crashed agent, expired lease)",
				storage.ErrNotOwner, id, current.Assignee)
		}
		return errUnclaimRowMoved(id, oldIssue.Status, current.Status)
	}

	return finishUnclaimInTx(ctx, tx, eventTable, id, actor, oldIssue, nextStatus)
}

// errUnclaimRowMoved is the error when the release UPDATE matched no row even
// though the checks before it passed. The row changed between the read and the
// write, so say what it changed to rather than a bare "no matching row".
func errUnclaimRowMoved(id string, readStatus, nowStatus types.Status) error {
	if readStatus != nowStatus {
		return fmt.Errorf("failed to unclaim issue %s: its status moved from %q to %q while it was being released; read it again and retry", id, readStatus, nowStatus)
	}
	return fmt.Errorf("failed to unclaim issue %s: no matching row", id)
}

// finishUnclaimInTx applies the post-UPDATE half of a release shared by
// UnclaimIssueInTx and UnclaimIssueIfAssigneeInTx: it drops the lease row (a
// no-op when none exists, e.g. a wisp or an open-but-assigned issue that was
// never leased) and records the "unclaimed" event. The row mutation
// (assignee/status/started_at/row_lock) must already have been applied in tx.
func finishUnclaimInTx(ctx context.Context, tx DBTX, eventTable string, id string, actor string, oldIssue *types.Issue, newStatus types.Status) error {
	if err := DeleteLeaseInTx(ctx, tx, id); err != nil {
		return err
	}

	oldData, _ := json.Marshal(oldIssue)
	newData, _ := json.Marshal(map[string]interface{}{
		"assignee": "",
		"status":   string(newStatus),
	})
	if err := RecordFullEventInTable(ctx, tx, eventTable, id, "unclaimed", actor, string(oldData), string(newData)); err != nil {
		return fmt.Errorf("failed to record unclaim event: %w", err)
	}
	// A release changes assignee and status, so it journals as an update. Both
	// unclaim entry points funnel through here after their CAS succeeded, so
	// this covers the conditional release too.
	return RecordEventInTx(ctx, tx, EventUpdate, id, actor)
}

// UnclaimIssueIfAssigneeInTx atomically releases a claim only while the issue is
// still assigned to expectedAssignee — the compare-and-swap inverse of
// ClaimIssueInTx: a Go-side actorMatches precheck (ga-5ksp5) plus a conditional
// UPDATE CASed on row_lock, with RowsAffected as the verdict, so a stale
// releaser can never clobber a claim that has since moved to (or been
// re-taken by) someone else. "Still assigned to expectedAssignee" is judged
// under actorMatches, not verbatim equality, so a caller naming the current
// holder under a different layer's spelling of the same identity is a match,
// not a mismatch — see canonicalActor. On success it applies the same
// transition as UnclaimIssueInTx (assignee cleared, status set by
// statusAfterUnclaim, started_at cleared, lease dropped, row_lock rewritten,
// "unclaimed" event recorded). When the current assignee does not match expectedAssignee —
// including when the issue is no longer assigned at all — it returns
// storage.ErrAssigneeMismatch naming the current holder and leaves the row
// untouched. actor is recorded as the event author.
//
//nolint:gosec // G201: table names come from WispTableRouting (hardcoded constants)
func UnclaimIssueIfAssigneeInTx(ctx context.Context, tx DBTX, id string, actor string, expectedAssignee string) error {
	if expectedAssignee == "" {
		return fmt.Errorf("conditional unclaim of %s: expected assignee must not be empty (use UnclaimIssueInTx for an unconditional release)", id)
	}

	// Route to the correct table (issues/wisps) automatically, matching
	// UnclaimIssueInTx.
	isWisp := IsActiveWispInTx(ctx, tx, id)
	issueTable, _, eventTable, _ := WispTableRouting(isWisp)

	oldIssue, err := GetIssueInTx(ctx, tx, id)
	if err != nil {
		return fmt.Errorf("failed to get issue for unclaim: %w", err)
	}

	// Validate: the status must be one a release can undo. See
	// statusAfterUnclaim.
	nextStatus, ok := statusAfterUnclaim(oldIssue.Status)
	if !ok {
		return errUnclaimStatus(id, oldIssue.Status)
	}

	// Compare-and-swap precheck: a mismatched holder — including an
	// already-released issue (empty assignee) — is a loud, typed no-op. Judged
	// under actorMatches (ga-5ksp5), not verbatim equality, so expectedAssignee
	// spelled under a different layer's separator convention than the stored
	// assignee still counts as a match. The read and the UPDATE below run in
	// the same transaction, so this check and the CAS WHERE clause see the
	// same row state.
	if !actorMatches(oldIssue.Assignee, expectedAssignee) {
		return fmt.Errorf("%w: %s is held by %q, expected %q", storage.ErrAssigneeMismatch, id, oldIssue.Assignee, expectedAssignee)
	}

	now := time.Now().UTC()

	// Atomic UPDATE CASed on row_lock rather than assignee (ga-5ksp5): the
	// Go-side check above already authorized the swap under actorMatches
	// against the row read into oldIssue, and row_lock is rewritten by every
	// path that mutates status/assignee/started_at (see the freshRowLock
	// invariant in lease.go) — so requiring it to still equal
	// oldIssue.RowVersion applies the same transition as UnclaimIssueInTx
	// (assignee cleared, status set by statusAfterUnclaim, started_at cleared,
	// row_lock rewritten) while detecting a racing reclaim/close on the same
	// row exactly as precisely as the old `assignee = <expectedAssignee>`
	// predicate did, without embedding a spelling-sensitive string comparison
	// in SQL. The status is pinned to the one read, so a deferred issue stays
	// deferred (vn-mpifu59).
	result, err := tx.ExecContext(ctx, fmt.Sprintf(`
		UPDATE %s
		SET assignee = '', status = ?, updated_at = ?,
		    started_at = NULL, row_lock = ?
		WHERE id = ? AND status = ? AND row_lock = ?
	`, issueTable), nextStatus, now, freshRowLock(), id, oldIssue.Status, oldIssue.RowVersion)
	if err != nil {
		return fmt.Errorf("failed to unclaim issue: %w", err)
	}

	rowsAffected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("failed to get rows affected: %w", err)
	}

	if rowsAffected == 0 {
		// The precheck passed and the read + UPDATE share this transaction, so a
		// 0-row result is not an assignee race (the row cannot change under us
		// mid-tx). Re-read and disambiguate, mirroring UnclaimIssueInTx: a
		// mismatched holder (under actorMatches) is the CAS verdict
		// (ErrAssigneeMismatch), otherwise the status is no longer releasable.
		current, gerr := GetIssueInTx(ctx, tx, id)
		if gerr != nil {
			return fmt.Errorf("failed to unclaim issue %s: no matching row", id)
		}
		if !actorMatches(current.Assignee, expectedAssignee) {
			return fmt.Errorf("%w: %s is held by %q, expected %q", storage.ErrAssigneeMismatch, id, current.Assignee, expectedAssignee)
		}
		return errUnclaimRowMoved(id, oldIssue.Status, current.Status)
	}

	return finishUnclaimInTx(ctx, tx, eventTable, id, actor, oldIssue, nextStatus)
}
