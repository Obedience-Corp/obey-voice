//go:build windows

package audiojob

import "os/exec"

// The reviewed runtime is Apple Silicon only. CommandContext still bounds
// protocol fixtures and rejects unsupported helper platforms before inference.
func configureWaveformCancellation(cmd *exec.Cmd) {}
