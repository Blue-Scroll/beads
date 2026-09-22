package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/spf13/cobra"
	"github.com/steveyegge/beads/internal/metrics"
	"github.com/steveyegge/beads/internal/types"
	"github.com/steveyegge/beads/internal/ui"
)

var humanCmd = &cobra.Command{
	Use:     "human",
	GroupID: "setup",
	Short:   "Show essential commands for human users",
	Long: `Display a focused help menu showing only the most common commands.

bd has 70+ commands - many for AI agents, integrations, and advanced workflows.
This command shows the ~15 essential commands that human users need most often.

For the full command list, run: bd --help

SUBCOMMANDS:
  human list              List all human-needed beads (issues with 'human' label)
  human respond <id>      Respond to a human-needed bead (adds comment and closes)
  human dismiss <id>      Dismiss a human-needed bead permanently
  human stats             Show summary statistics for human-needed beads`,
	Run: func(cmd *cobra.Command, args []string) {
		evt := metrics.NewCommandEvent("human")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		fmt.Printf("\n%s\n", ui.RenderBold("bd - Essential Commands for Humans"))
		fmt.Printf("For all 70+ commands: bd --help\n\n")

		// Issues - Core workflow
		fmt.Printf("%s\n", ui.RenderAccent("Working With Issues:"))
		printCmd("create", "Create a new issue")
		printCmd("list", "List issues (filter with --status, --priority, --label)")
		printCmd("show <id>", "Show issue details")
		printCmd("update <id>", "Update an issue (--status, --priority, --assignee)")
		printCmd("close <id>", "Close one or more issues")
		printCmd("reopen <id>", "Reopen a closed issue")
		printCmd("note <id> <text>", "Add a note to an issue (or: comments add <id>)")
		fmt.Println()

		// Workflow
		fmt.Printf("%s\n", ui.RenderAccent("Finding Work:"))
		printCmd("ready", "Show issues ready to work on (no blockers)")
		printCmd("search <query>", "Search issues by text")
		printCmd("status", "Show project overview and counts")
		printCmd("stats", "Show detailed statistics")
		fmt.Println()

		// Dependencies
		fmt.Printf("%s\n", ui.RenderAccent("Dependencies:"))
		printCmd("dep add <a> <b>", "Add dependency (a depends on b)")
		printCmd("dep remove <a> <b>", "Remove a dependency")
		printCmd("dep tree <id>", "Show dependency tree")
		printCmd("graph", "Display visual dependency graph")
		printCmd("blocked", "Show all blocked issues")
		fmt.Println()

		// Setup & Maintenance
		fmt.Printf("%s\n", ui.RenderAccent("Setup & Maintenance:"))
		printCmd("init", "Initialize bd in current directory")
		printCmd("doctor", "Check installation health")
		fmt.Println()

		// Help
		fmt.Printf("%s\n", ui.RenderAccent("Getting Help:"))
		printCmd("quickstart", "Quick start guide with examples")
		printCmd("help <cmd>", "Help for any command")
		printCmd("--help", "Full command list (70+ commands)")
		fmt.Println()

		// Common examples
		fmt.Printf("%s\n", ui.RenderAccent("Quick Examples:"))
		fmt.Printf("  %s\n", ui.RenderMuted("# Create and track an issue"))
		fmt.Printf("  bd create \"Fix login bug\" --priority 1\n")
		fmt.Printf("  bd update bd-abc123 --claim\n")
		fmt.Printf("  bd close bd-abc123\n\n")

		fmt.Printf("  %s\n", ui.RenderMuted("# See what needs doing"))
		fmt.Printf("  bd ready                    # What can I work on?\n")
		fmt.Printf("  bd list --status open       # All open issues\n")
		fmt.Printf("  bd blocked                  # What's stuck?\n\n")
	},
}

// human list command
var humanListCmd = &cobra.Command{
	Use:   "list",
	Short: "List all human-needed beads",
	Long: `List all issues labeled with 'human' tag.

These are issues that require human intervention or input.

Every row prints its Status, and the header prints how many beads are still
waiting on a person. Read that number. Do not count rows yourself.

A bead waits on a human until it is CLOSED, so --status=open is not the
waiting list: it hides in_progress, blocked and deferred beads that nobody
has answered. Use --waiting for every bead that still needs an answer.

Examples:
  bd human list
  bd human list --waiting
  bd human list --status=open
  bd human list --json`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("human-list")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		status, _ := cmd.Flags().GetString("status")
		waiting, _ := cmd.Flags().GetBool("waiting")

		if waiting && status != "" {
			return HandleErrorRespectJSON("--waiting and --status ask two different questions: use one. --waiting is every bead that is not closed")
		}

		ctx := rootCtx

		filter := types.IssueFilter{
			Labels: []string{"human"},
		}

		if status != "" {
			s := types.Status(status)
			filter.Status = &s
		}

		if err := ensureStoreActive(); err != nil {
			return HandleErrorRespectJSON("listing human beads: %v", err)
		}

		var err error
		issues, err := store.SearchIssues(ctx, "", filter)
		if err != nil {
			return HandleErrorRespectJSON("listing human beads: %v", err)
		}

		if waiting {
			issues = filterWaitingOnHuman(issues)
		}

		if jsonOutput {
			issueIDs := make([]string, len(issues))
			for i, issue := range issues {
				issueIDs[i] = issue.ID
			}
			labelsMap, _ := store.GetLabelsForIssues(ctx, issueIDs)
			for _, issue := range issues {
				issue.Labels = labelsMap[issue.ID]
			}

			data, err := json.MarshalIndent(issues, "", "  ")
			if err != nil {
				return HandleErrorRespectJSON("encoding JSON: %v", err)
			}
			fmt.Println(string(data))
			return nil
		}

		printHumanList(issues)
		return nil
	},
}

// isWaitingOnHuman says whether a bead still needs a person to answer it.
// A bead waits until it is closed. Every other status (open, in_progress,
// blocked, deferred, hooked) is still somebody's question. The list and the
// stats both call this, so the two surfaces can never disagree about a count.
func isWaitingOnHuman(issue *types.Issue) bool {
	return issue.Status != types.StatusClosed
}

// filterWaitingOnHuman keeps only the beads that still need an answer.
func filterWaitingOnHuman(issues []*types.Issue) []*types.Issue {
	kept := make([]*types.Issue, 0, len(issues))
	for _, issue := range issues {
		if isWaitingOnHuman(issue) {
			kept = append(kept, issue)
		}
	}
	return kept
}

// humanWaitingSummary is the one line that answers "how many decisions are
// waiting?" without anybody counting rows by hand. Read this number instead of
// grepping the rows below it.
func humanWaitingSummary(issues []*types.Issue) string {
	waiting := 0
	answered := 0
	byStatus := map[types.Status]int{}
	var order []types.Status

	for _, issue := range issues {
		if !isWaitingOnHuman(issue) {
			answered++
			continue
		}
		waiting++
		if byStatus[issue.Status] == 0 {
			order = append(order, issue.Status)
		}
		byStatus[issue.Status]++
	}

	parts := make([]string, 0, len(order))
	for _, st := range order {
		parts = append(parts, fmt.Sprintf("%s %d", st, byStatus[st]))
	}

	summary := fmt.Sprintf("Waiting on a human: %d", waiting)
	if len(parts) > 0 {
		summary += fmt.Sprintf(" (%s)", strings.Join(parts, ", "))
	}
	return summary + fmt.Sprintf(". Already answered: %d.", answered)
}

func printHumanList(issues []*types.Issue) {
	fprintHumanList(os.Stdout, issues)
}

// fprintHumanList writes the list to w. It takes a writer so a test can read
// what a person actually sees; a printer that only writes to stdout can only
// be tested for "it did not panic", which is how the missing Status line
// below survived (hq-vjm31n).
func fprintHumanList(w io.Writer, issues []*types.Issue) {
	if len(issues) == 0 {
		fmt.Fprintln(w, "No human-needed beads found.")
		return
	}

	fmt.Fprintf(w, "\n%s (%d found)\n", ui.RenderBold("Human-needed beads"), len(issues))
	fmt.Fprintf(w, "  %s\n\n", humanWaitingSummary(issues))
	for _, issue := range issues {
		fmt.Fprintf(w, "  %s %s\n", ui.RenderCommand(issue.ID), issue.Title)
		// EVERY bead prints a Status line, open included. This used to skip
		// "open", so a count built by grepping "Status:" counted every bead
		// EXCEPT the ones still waiting on a person. The count did not read
		// low, it read zero: 28 real decisions were invisible for weeks while
		// patrols reported the queue clear (hq-vjm31n, measured 2026-09-22).
		fmt.Fprintf(w, "    Status: %s\n", issue.Status)
		// P0 is a real priority, and it is the most urgent one. Printing it
		// only when non-zero hid exactly the rows that matter most.
		fmt.Fprintf(w, "    Priority: P%d\n", issue.Priority)
		fmt.Fprintln(w)
	}
}

// human respond command
var humanRespondCmd = &cobra.Command{
	Use:   "respond <issue-id>",
	Short: "Respond to a human-needed bead",
	Long: `Respond to a human-needed bead by adding a comment and closing it.

The response is added as a comment and the issue is closed with reason "Responded".

Examples:
  bd human respond bd-123 --response "Use OAuth2 for authentication"
  bd human respond bd-123 -r "Approved, proceed with implementation"`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("human-respond")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		response, _ := cmd.Flags().GetString("response")

		if response == "" {
			return HandleErrorRespectJSON("--response is required")
		}

		CheckReadonly("human respond")

		ctx := rootCtx
		issueID := args[0]

		// Direct mode
		if err := ensureStoreActive(); err != nil {
			return HandleErrorRespectJSON("responding to human bead: %v", err)
		}

		// Resolve partial ID and get issue
		result, err := resolveAndGetIssueForMutation(ctx, store, issueID)
		if err != nil {
			return HandleErrorRespectJSON("resolving issue ID %s: %v", issueID, err)
		}
		if result == nil || result.Issue == nil {
			if result != nil {
				result.Close()
			}
			return HandleErrorRespectJSON("issue not found: %s", issueID)
		}
		defer result.Close()

		resolvedID := result.ResolvedID
		issue := result.Issue
		targetStore := result.Store

		if issue.Status == "closed" {
			return HandleErrorRespectJSON("issue %s is already closed", resolvedID)
		}

		labelsMap, _ := targetStore.GetLabelsForIssues(ctx, []string{resolvedID})
		hasHumanLabel := false
		for _, label := range labelsMap[resolvedID] {
			if label == "human" {
				hasHumanLabel = true
				break
			}
		}

		if !hasHumanLabel {
			fmt.Fprintf(os.Stderr, "Warning: Issue %s does not have 'human' label\n", resolvedID)
		}

		commentText := fmt.Sprintf("Response: %s", response)
		_, err = targetStore.AddIssueComment(ctx, resolvedID, actor, commentText)
		if err != nil {
			return HandleErrorRespectJSON("adding comment: %v", err)
		}

		if err := targetStore.CloseIssue(ctx, resolvedID, "Responded", actor, ""); err != nil {
			return HandleErrorRespectJSON("closing bead: %v", err)
		}

		fmt.Printf("%s Bead %s closed with response.\n", ui.RenderPass("✔"), resolvedID)
		return nil
	},
}

// human dismiss command
var humanDismissCmd = &cobra.Command{
	Use:   "dismiss <issue-id>",
	Short: "Dismiss a human-needed bead",
	Long: `Dismiss a human-needed bead permanently without responding.

The issue is closed with a "Dismissed" reason and optional note.

Examples:
  bd human dismiss bd-123
  bd human dismiss bd-123 --reason "No longer applicable"`,
	Args:          cobra.ExactArgs(1),
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("human-dismiss")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		reason, _ := cmd.Flags().GetString("reason")

		CheckReadonly("human dismiss")

		ctx := rootCtx
		issueID := args[0]

		// Direct mode
		if err := ensureStoreActive(); err != nil {
			return HandleErrorRespectJSON("dismissing human bead: %v", err)
		}

		// Resolve partial ID and get issue
		result, err := resolveAndGetIssueForMutation(ctx, store, issueID)
		if err != nil {
			return HandleErrorRespectJSON("resolving issue ID %s: %v", issueID, err)
		}
		if result == nil || result.Issue == nil {
			if result != nil {
				result.Close()
			}
			return HandleErrorRespectJSON("issue not found: %s", issueID)
		}
		defer result.Close()

		resolvedID := result.ResolvedID
		issue := result.Issue
		targetStore := result.Store

		if issue.Status == "closed" {
			return HandleErrorRespectJSON("issue %s is already closed", resolvedID)
		}

		labelsMap, _ := targetStore.GetLabelsForIssues(ctx, []string{resolvedID})
		hasHumanLabel := false
		for _, label := range labelsMap[resolvedID] {
			if label == "human" {
				hasHumanLabel = true
				break
			}
		}

		if !hasHumanLabel {
			fmt.Fprintf(os.Stderr, "Warning: Issue %s does not have 'human' label\n", resolvedID)
		}

		closeReason := "Dismissed"
		if reason != "" {
			closeReason = fmt.Sprintf("Dismissed: %s", reason)
		}

		if err := targetStore.CloseIssue(ctx, resolvedID, closeReason, actor, ""); err != nil {
			return HandleErrorRespectJSON("closing bead: %v", err)
		}

		fmt.Printf("%s Bead %s dismissed.\n", ui.RenderPass("✔"), resolvedID)
		return nil
	},
}

// human stats command
var humanStatsCmd = &cobra.Command{
	Use:   "stats",
	Short: "Show summary statistics for human-needed beads",
	Long: `Display summary statistics for human-needed beads.

Shows counts for total, pending, responded (closed without dismiss), and
dismissed beads. Pending is every bead that is NOT closed, so it counts
in_progress, blocked and deferred beads too, not just open ones.

Example:
  bd human stats`,
	SilenceUsage:  true,
	SilenceErrors: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		evt := metrics.NewCommandEvent("human-stats")
		defer func() {
			if c := metrics.Global(); c != nil {
				c.CloseEventAndAdd(evt)
			}
		}()

		ctx := rootCtx

		filter := types.IssueFilter{
			Labels: []string{"human"},
		}

		if err := ensureStoreActive(); err != nil {
			return HandleErrorRespectJSON("getting human bead stats: %v", err)
		}

		var err error
		issues, err := store.SearchIssues(ctx, "", filter)
		if err != nil {
			return HandleErrorRespectJSON("getting human bead stats: %v", err)
		}

		printHumanStats(issues)
		return nil
	},
}

func printHumanStats(issues []*types.Issue) {
	total := len(issues)
	pending := 0
	closed := 0
	dismissed := 0

	for _, issue := range issues {
		// Same predicate as the list, so the two surfaces cannot disagree.
		if isWaitingOnHuman(issue) {
			pending++
			continue
		}
		closed++
		if strings.Contains(strings.ToLower(issue.CloseReason), "dismiss") {
			dismissed++
		}
	}

	responded := closed - dismissed

	fmt.Printf("\n%s\n", ui.RenderBold("Human Beads Stats"))
	fmt.Println()
	fmt.Printf("  Total:      %d\n", total)
	fmt.Printf("  Pending:    %d\n", pending)
	fmt.Printf("  Responded:  %d\n", responded)
	fmt.Printf("  Dismissed:  %d\n", dismissed)
	fmt.Println()
}

// printCmd prints a command with consistent formatting
func printCmd(cmd, description string) {
	fmt.Printf("  %-20s %s\n", ui.RenderCommand(cmd), description)
}

func init() {
	// Add subcommands to humanCmd
	humanCmd.AddCommand(humanListCmd)
	humanCmd.AddCommand(humanRespondCmd)
	humanCmd.AddCommand(humanDismissCmd)
	humanCmd.AddCommand(humanStatsCmd)

	// Add flags for subcommands
	humanListCmd.Flags().StringP("status", "s", "", "Filter by ONE status. Not the waiting list: --status=open hides in_progress, blocked and deferred beads nobody has answered. Use --waiting for those")
	humanListCmd.Flags().Bool("waiting", false, "Show only beads still waiting on a human (every status except closed)")
	humanRespondCmd.Flags().StringP("response", "r", "", "Response text (required)")
	_ = humanRespondCmd.MarkFlagRequired("response")
	humanDismissCmd.Flags().StringP("reason", "", "", "Reason for dismissal (optional)")

	// Register with root command
	rootCmd.AddCommand(humanCmd)
}
