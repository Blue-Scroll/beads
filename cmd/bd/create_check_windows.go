//go:build windows

package main

import "os/exec"

// setCreateCheckProcessGroup does nothing on Windows, which has no Unix
// process groups. On a timeout exec kills the started process only; a child
// that detached may survive. This matches internal/hooks on Windows.
func setCreateCheckProcessGroup(cmd *exec.Cmd) {}
