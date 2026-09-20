package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/deps"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
	"github.com/steveyegge/beads/internal/utils"
)

var duplicateCmd = &cobra.Command{
	Use:     "duplicate <id> --of <canonical>",
	GroupID: "deps",
	Short:   "Mark an issue as a duplicate of another",
	Long: `Mark an issue as a duplicate of a canonical issue.

The duplicate issue is automatically closed with a reference to the canonical.
This is essential for large issue databases with many similar reports.

Anything held back behind the duplicate is moved onto the canonical issue
first, so closing the duplicate never releases work nobody cleared. Only
blocking edges move, and only for open issues. If the canonical issue is itself
closed while something is still held back, the command refuses and writes
nothing: a closed issue holds nothing back.

Examples:
  bd duplicate bd-abc --of bd-xyz    # Mark bd-abc as duplicate of bd-xyz`,
	Args: cobra.ExactArgs(1),
	RunE: runDuplicate,
}

var supersedeCmd = &cobra.Command{
	Use:     "supersede <id> --with <new>",
	GroupID: "deps",
	Short:   "Mark an issue as superseded by a newer one",
	Long: `Mark an issue as superseded by a newer version.

The superseded issue is automatically closed with a reference to the replacement.
Useful for design docs, specs, and evolving artifacts.

Anything held back behind the superseded issue is moved onto the replacement
first, so closing it never releases work nobody cleared. Only blocking edges
move, and only for open issues. If the replacement is itself closed while
something is still held back, the command refuses and writes nothing: a closed
issue holds nothing back.

Examples:
  bd supersede bd-old --with bd-new    # Mark bd-old as superseded by bd-new`,
	Args: cobra.ExactArgs(1),
	RunE: runSupersede,
}

var (
	duplicateOf    string
	supersededWith string
)

func init() {
	duplicateCmd.Flags().StringVar(&duplicateOf, "of", "", "Canonical issue ID (required)")
	_ = duplicateCmd.MarkFlagRequired("of") // Only fails if flag missing (caught in tests)
	duplicateCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(duplicateCmd)

	supersedeCmd.Flags().StringVar(&supersededWith, "with", "", "Replacement issue ID (required)")
	_ = supersedeCmd.MarkFlagRequired("with") // Only fails if flag missing (caught in tests)
	supersedeCmd.ValidArgsFunction = issueIDCompletion
	rootCmd.AddCommand(supersedeCmd)
}

func runDuplicate(cmd *cobra.Command, args []string) error {
	if usesProxiedServer() {
		return HandleErrorRespectJSON("duplicate is not supported in proxied-server mode")
	}
	CheckReadonly("duplicate")

	evt := metrics.NewCommandEvent("duplicate")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	ctx := getRootContext()
	store := getStore()
	actor := getActor()

	// Resolve partial IDs
	var duplicateID, canonicalID string
	var err error
	duplicateID, err = utils.ResolvePartialID(ctx, store, args[0])
	if err != nil {
		return fmt.Errorf("failed to resolve %s: %w", args[0], err)
	}
	canonicalID, err = utils.ResolvePartialID(ctx, store, duplicateOf)
	if err != nil {
		return fmt.Errorf("failed to resolve %s: %w", duplicateOf, err)
	}

	if duplicateID == canonicalID {
		return fmt.Errorf("cannot mark an issue as duplicate of itself")
	}

	// Verify canonical issue exists
	var canonical *types.Issue
	canonical, err = store.GetIssue(ctx, canonicalID)
	if err != nil || canonical == nil {
		return fmt.Errorf("canonical issue not found: %s", canonicalID)
	}

	// Move the fences BEFORE anything closes. Everything held back behind the
	// duplicate is released the moment it closes, and a work pool can hand a
	// released issue out within minutes with nothing going red.
	moved, err := moveFences(ctx, store, duplicateID, canonical, actor)
	if err != nil {
		return err
	}

	// Add a "duplicates" dependency edge (duplicate → canonical)
	dep := &types.Dependency{
		IssueID:     duplicateID,
		DependsOnID: canonicalID,
		Type:        types.DepDuplicates,
	}
	if err := store.AddDependency(ctx, dep, actor); err != nil {
		return fmt.Errorf("failed to add duplicate link: %w", err)
	}

	// Close the duplicate issue through the lifecycle operation so it records
	// the complete closure state.
	if err := store.CloseIssue(ctx, duplicateID, "", actor, ""); err != nil {
		return fmt.Errorf("failed to close duplicate: %w", err)
	}

	commandDidWrite.Store(true)

	if isJSONOutput() {
		return outputJSON(map[string]interface{}{
			"duplicate": duplicateID,
			"canonical": canonicalID,
			"status":    "closed",
			"moved":     moved,
		})
	}

	fmt.Printf("%s Marked %s as duplicate of %s (closed)\n", ui.RenderPass("✓"), duplicateID, canonicalID)
	printFenceMoves(os.Stdout, moved, canonicalID)
	return nil
}

func runSupersede(cmd *cobra.Command, args []string) error {
	if usesProxiedServer() {
		return HandleErrorRespectJSON("supersede is not supported in proxied-server mode")
	}
	CheckReadonly("supersede")

	evt := metrics.NewCommandEvent("supersede")
	defer func() {
		if c := metrics.Global(); c != nil {
			c.CloseEventAndAdd(evt)
		}
	}()

	ctx := getRootContext()
	store := getStore()
	actor := getActor()

	// Resolve partial IDs
	var oldID, newID string
	var err error
	oldID, err = utils.ResolvePartialID(ctx, store, args[0])
	if err != nil {
		return fmt.Errorf("failed to resolve %s: %w", args[0], err)
	}
	newID, err = utils.ResolvePartialID(ctx, store, supersededWith)
	if err != nil {
		return fmt.Errorf("failed to resolve %s: %w", supersededWith, err)
	}

	if oldID == newID {
		return fmt.Errorf("cannot mark an issue as superseded by itself")
	}

	// Verify new issue exists
	var newIssue *types.Issue
	newIssue, err = store.GetIssue(ctx, newID)
	if err != nil || newIssue == nil {
		return fmt.Errorf("replacement issue not found: %s", newID)
	}

	// Move the fences BEFORE anything closes. See runDuplicate above.
	moved, err := moveFences(ctx, store, oldID, newIssue, actor)
	if err != nil {
		return err
	}

	// Add a "supersedes" dependency edge (old → new)
	dep := &types.Dependency{
		IssueID:     oldID,
		DependsOnID: newID,
		Type:        types.DepSupersedes,
	}
	if err := store.AddDependency(ctx, dep, actor); err != nil {
		return fmt.Errorf("failed to add supersede link: %w", err)
	}

	// Close the superseded issue through the lifecycle operation so it records
	// the complete closure state.
	if err := store.CloseIssue(ctx, oldID, "", actor, ""); err != nil {
		return fmt.Errorf("failed to close superseded issue: %w", err)
	}

	commandDidWrite.Store(true)

	if isJSONOutput() {
		return outputJSON(map[string]interface{}{
			"superseded":  oldID,
			"replacement": newID,
			"status":      "closed",
			"moved":       moved,
		})
	}

	fmt.Printf("%s Marked %s as superseded by %s (closed)\n", ui.RenderPass("✓"), oldID, newID)
	printFenceMoves(os.Stdout, moved, newID)
	return nil
}

// moveFences re-points everything that is held back behind dyingID so that it
// is held behind the survivor instead. Both duplicate and supersede call it,
// because both close an issue that other work may be waiting on.
//
// A half-done run is still reported: the moves that landed go to stderr before
// the error, so nobody has to guess which fences are where.
func moveFences(ctx context.Context, store deps.RetargetStore, dyingID string, survivor *types.Issue, actor string) ([]deps.Move, error) {
	moved, err := deps.Retarget(ctx, store, dyingID, survivor, actor)
	if len(moved) > 0 {
		commandDidWrite.Store(true)
	}
	if err != nil {
		printFenceMoves(os.Stderr, moved, survivor.ID)
		return moved, err
	}
	if moved == nil {
		// Never nil. A --json reader should see an empty list, not null.
		moved = []deps.Move{}
	}
	return moved, nil
}

// printFenceMoves says what moved. A move nobody can see is how a dropped
// fence stays invisible.
func printFenceMoves(w io.Writer, moved []deps.Move, survivorID string) {
	if len(moved) == 0 {
		return
	}
	fmt.Fprintf(w, "  Moved %d fence(s) onto %s, so nothing was released:\n", len(moved), survivorID)
	for _, m := range moved {
		switch m.Kind {
		case deps.MoveDroppedSelf:
			fmt.Fprintf(w, "    %s: dropped its %s edge (it IS %s, so there is no new edge)\n", m.Dependent, m.Type, survivorID)
		case deps.MoveAlreadyHeld:
			fmt.Fprintf(w, "    %s: already held behind %s, dropped the old %s edge\n", m.Dependent, survivorID, m.Type)
		default:
			fmt.Fprintf(w, "    %s: %s now points at %s\n", m.Dependent, m.Type, survivorID)
		}
	}
}
