---
title: "bd human"
description: "Display a focused help menu showing only the most common commands."
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc human`.

Display a focused help menu showing only the most common commands.

bd has 70+ commands - many for AI agents, integrations, and advanced workflows.
This command shows the ~15 essential commands that human users need most often.

For the full command list, run: bd --help

SUBCOMMANDS:
  human list              List all human-needed beads (issues with 'human' label)
  human respond &lt;id&gt;      Respond to a human-needed bead (adds comment and closes)
  human dismiss &lt;id&gt;      Dismiss a human-needed bead permanently
  human stats             Show summary statistics for human-needed beads

```
bd human [flags]
```

## bd human dismiss

Dismiss a human-needed bead permanently without responding.

The issue is closed with a "Dismissed" reason and optional note.

Examples:
  bd human dismiss bd-123
  bd human dismiss bd-123 --reason "No longer applicable"

```
bd human dismiss <issue-id> [flags]
```

**Flags:**

```
      --reason string   Reason for dismissal (optional)
```

## bd human list

List all issues labeled with 'human' tag.

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
  bd human list --json

```
bd human list [flags]
```

**Flags:**

```
  -s, --status string   Filter by ONE status. Not the waiting list: --status=open hides in_progress, blocked and deferred beads nobody has answered. Use --waiting for those
      --waiting         Show only beads still waiting on a human (every status except closed)
```

## bd human respond

Respond to a human-needed bead by adding a comment and closing it.

The response is added as a comment and the issue is closed with reason "Responded".

Examples:
  bd human respond bd-123 --response "Use OAuth2 for authentication"
  bd human respond bd-123 -r "Approved, proceed with implementation"

```
bd human respond <issue-id> [flags]
```

**Flags:**

```
  -r, --response string   Response text (required)
```

## bd human stats

Display summary statistics for human-needed beads.

Shows counts for total, pending, responded (closed without dismiss), and
dismissed beads. Pending is every bead that is NOT closed, so it counts
in_progress, blocked and deferred beads too, not just open ones.

Example:
  bd human stats

```
bd human stats [flags]
```
