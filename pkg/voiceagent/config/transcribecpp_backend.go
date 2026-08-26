package config

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// transcribeCPPGPUKinds are the --list-devices kinds that count as a GPU lane.
// The distinction is not cosmetic: measured on the same Parakeet weights,
// transcribe.cpp on Metal is 3.1x faster than the default sherpa-parakeet, but
// on CPU it is ~1.4x SLOWER. --backend auto falls back to CPU silently, so a
// machine with no GPU lane must be refused rather than quietly regressed.
var transcribeCPPGPUKinds = map[string]bool{
	"metal":  true,
	"cuda":   true,
	"vulkan": true,
	"rocm":   true,
}

// ComputeLane is one compute device transcribe-cli reports.
type ComputeLane struct {
	Kind string // metal, cuda, vulkan, rocm, cpu, ...
	Name string // device name, e.g. MTL0
	GPU  bool
}

// TranscribeCPPLanes runs `<binary> --list-devices` and reports the compute
// lanes it offers. The CLI prints one indented "name=... kind=... type=..."
// line per device; unparseable lines are ignored rather than failing the probe.
func TranscribeCPPLanes(ctx context.Context, binary string) ([]ComputeLane, error) {
	out, err := exec.CommandContext(ctx, binary, "--list-devices").CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("transcribe.cpp %s --list-devices: %w (%s)", binary, err, strings.TrimSpace(string(out)))
	}
	return parseComputeLanes(string(out)), nil
}

func parseComputeLanes(out string) []ComputeLane {
	var lanes []ComputeLane
	for _, line := range strings.Split(out, "\n") {
		fields := map[string]string{}
		for _, tok := range strings.Fields(line) {
			if k, v, ok := strings.Cut(tok, "="); ok {
				fields[k] = v
			}
		}
		kind := strings.ToLower(fields["kind"])
		if kind == "" {
			continue
		}
		lanes = append(lanes, ComputeLane{
			Kind: kind,
			Name: fields["name"],
			GPU:  transcribeCPPGPUKinds[kind] || strings.ToLower(fields["type"]) == "gpu",
		})
	}
	return lanes
}

// GPULanes returns the kinds of the GPU lanes in lanes, in listed order.
func GPULanes(lanes []ComputeLane) []string {
	var kinds []string
	for _, l := range lanes {
		if l.GPU {
			kinds = append(kinds, l.Kind)
		}
	}
	return kinds
}

// LaneKinds returns every lane kind in listed order, for diagnostics.
func LaneKinds(lanes []ComputeLane) []string {
	kinds := make([]string, 0, len(lanes))
	for _, l := range lanes {
		kinds = append(kinds, l.Kind)
	}
	return kinds
}

// TranscribeCPPCPUOnlyError is the shared refusal for a machine whose
// transcribe-cli offers no GPU lane. The provider and doctor must say the same
// thing, so both build it here.
func TranscribeCPPCPUOnlyError(lanes []ComputeLane) error {
	return fmt.Errorf("transcribe.cpp offers no GPU lane on this machine (devices: %s); on CPU it is slower than the default sherpa-parakeet, so it is refused rather than run — use a machine with Metal or CUDA, or set stt_provider back to sherpa-parakeet",
		strings.Join(LaneKinds(lanes), ", "))
}
