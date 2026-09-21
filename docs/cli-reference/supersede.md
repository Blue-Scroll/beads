---
title: "bd supersede"
description: "Mark an issue as superseded by a newer version."
---

{/* AUTO-GENERATED: do not edit manually */}

Generated from `bd help --doc supersede`.

Mark an issue as superseded by a newer version.

The superseded issue is automatically closed with a reference to the replacement.
Useful for design docs, specs, and evolving artifacts.

Anything held back behind the superseded issue is moved onto the replacement
first, so closing it never releases work nobody cleared. Only blocking edges
move, and only for open issues. If the replacement is itself closed while
something is still held back, the command refuses and writes nothing: a closed
issue holds nothing back.

Examples:
  bd supersede bd-old --with bd-new    # Mark bd-old as superseded by bd-new

```
bd supersede <id> --with <new> [flags]
```

**Flags:**

```
      --with string   Replacement issue ID (required)
```
