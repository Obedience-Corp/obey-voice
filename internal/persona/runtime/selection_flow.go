package personaruntime

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

//go:embed selection_flow.yaml
var selectionFlowBytes embed.FS

const supportedSelectionFlowVersion = 1

const selectionFlowPath = "selection_flow.yaml"

// SelectionActions are the serialized selection steps supplied by the owning
// session/selector. The flow only sequences these operations; it does not own
// persona configuration, persistence, or response resources.
type SelectionActions struct {
	Resolve func(context.Context) error
	Prepare func(context.Context) error
	Commit  func(context.Context) error
}

type selectionFlowDocument struct {
	Version int                       `yaml:"version"`
	Start   string                    `yaml:"start"`
	States  map[string]selectionState `yaml:"states"`
}

type selectionState struct {
	Action  *string `yaml:"action"`
	Success *string `yaml:"success"`
	Error   *string `yaml:"error"`
}

// SelectionFlowInfo reports the validated embedded flow's version and SHA-256
// digest. The flow is always loaded from the compiled-in asset; there is no
// user path or configuration override.
func SelectionFlowInfo() (version int, hash string, err error) {
	data, readErr := embeddedSelectionFlow()
	if readErr != nil {
		return 0, "", readErr
	}
	flow, parseErr := parseSelectionFlow(data)
	if parseErr != nil {
		return 0, "", parseErr
	}
	digest := sha256.Sum256(data)
	return flow.Version, hex.EncodeToString(digest[:]), nil
}

// RunSelectionFlow executes the validated embedded selection flow in order.
// Every callback failure is returned with its stage. Context cancellation is
// checked before each stage; after Commit returns successfully, its durable
// publication is considered complete even if cancellation raced with it.
func RunSelectionFlow(ctx context.Context, actions SelectionActions) error {
	data, err := embeddedSelectionFlow()
	if err != nil {
		return err
	}
	flow, err := parseSelectionFlow(data)
	if err != nil {
		return err
	}
	if ctx == nil {
		return errors.New("selection flow: nil context")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if actions.Resolve == nil {
		return errors.New("selection flow: resolve action is nil")
	}
	if actions.Prepare == nil {
		return errors.New("selection flow: prepare action is nil")
	}
	if actions.Commit == nil {
		return errors.New("selection flow: commit action is nil")
	}

	state := flow.Start
	for {
		current := flow.States[state]
		if current.Action == nil {
			if state == "committed" {
				return nil
			}
			return fmt.Errorf("selection flow: reached terminal state %q", state)
		}
		if err := ctx.Err(); err != nil {
			return err
		}

		var action func(context.Context) error
		switch *current.Action {
		case "resolve":
			action = actions.Resolve
		case "prepare":
			action = actions.Prepare
		case "commit":
			action = actions.Commit
		default:
			return fmt.Errorf("selection flow: unsupported action %q", *current.Action)
		}
		if err := action(ctx); err != nil {
			return fmt.Errorf("selection flow %s: %w", *current.Action, err)
		}
		// Do not inspect ctx after commit. A successful Commit is durable
		// publication, even when cancellation races with its return.
		if *current.Action != "commit" {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		state = *current.Success
	}
}

func embeddedSelectionFlow() ([]byte, error) {
	data, err := selectionFlowBytes.ReadFile(selectionFlowPath)
	if err != nil {
		return nil, fmt.Errorf("selection flow: read embedded asset: %w", err)
	}
	return data, nil
}

// parseSelectionFlow is intentionally unexported. Tests use it to validate
// malformed candidate assets without introducing a runtime path override.
func parseSelectionFlow(data []byte) (selectionFlowDocument, error) {
	var flow selectionFlowDocument
	decoder := yaml.NewDecoder(strings.NewReader(string(data)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&flow); err != nil {
		return selectionFlowDocument{}, fmt.Errorf("selection flow: parse YAML: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); err == nil {
		return selectionFlowDocument{}, errors.New("selection flow: YAML contains multiple documents")
	} else if !errors.Is(err, io.EOF) {
		return selectionFlowDocument{}, fmt.Errorf("selection flow: parse trailing YAML: %w", err)
	}
	if err := validateSelectionFlow(flow); err != nil {
		return selectionFlowDocument{}, err
	}
	return flow, nil
}

func validateSelectionFlow(flow selectionFlowDocument) error {
	if flow.Version != supportedSelectionFlowVersion {
		return fmt.Errorf("selection flow: unsupported schema version %d", flow.Version)
	}
	if flow.Start != "start" {
		return fmt.Errorf("selection flow: start state must be %q", "start")
	}
	wantStates := []string{"committed", "failed", "prepared", "resolved", "start"}
	if len(flow.States) != len(wantStates) {
		return fmt.Errorf("selection flow: supported states are %v", wantStates)
	}
	for _, state := range wantStates {
		if _, ok := flow.States[state]; !ok {
			return fmt.Errorf("selection flow: missing state %q", state)
		}
	}
	for state := range flow.States {
		if !slices.Contains(wantStates, state) {
			return fmt.Errorf("selection flow: unsupported state %q", state)
		}
	}
	for _, terminal := range []string{"committed", "failed"} {
		if state := flow.States[terminal]; state.Action != nil || state.Success != nil || state.Error != nil {
			return fmt.Errorf("selection flow: terminal state %q must have no action or transitions", terminal)
		}
	}
	expected := map[string]string{"start": "resolve", "resolved": "prepare", "prepared": "commit"}
	for state, action := range expected {
		current := flow.States[state]
		if current.Action == nil || *current.Action != action {
			return fmt.Errorf("selection flow: state %q must use action %q", state, action)
		}
		if current.Success == nil || current.Error == nil || *current.Success == "" || *current.Error == "" {
			return fmt.Errorf("selection flow: state %q requires exactly one success and error transition", state)
		}
		if *current.Error != "failed" {
			return fmt.Errorf("selection flow: action %q must fail to %q", action, "failed")
		}
	}
	if *flow.States["start"].Success != "resolved" || *flow.States["resolved"].Success != "prepared" || *flow.States["prepared"].Success != "committed" {
		return errors.New("selection flow: success transitions do not form start -> resolved -> prepared -> committed")
	}
	for state, current := range flow.States {
		if current.Action == nil {
			continue
		}
		for _, target := range []string{*current.Success, *current.Error} {
			if _, ok := flow.States[target]; !ok {
				return fmt.Errorf("selection flow: state %q targets unknown state %q", state, target)
			}
		}
	}
	if err := validateSelectionFlowReachability(flow); err != nil {
		return err
	}
	return nil
}

func validateSelectionFlowReachability(flow selectionFlowDocument) error {
	seen := make(map[string]bool, len(flow.States))
	active := make(map[string]bool, len(flow.States))
	var visit func(string) error
	visit = func(state string) error {
		if active[state] {
			return fmt.Errorf("selection flow: cycle detected at state %q", state)
		}
		if seen[state] {
			return nil
		}
		seen[state] = true
		active[state] = true
		current := flow.States[state]
		if current.Action == nil {
			delete(active, state)
			return nil
		}
		if err := visit(*current.Success); err != nil {
			return err
		}
		// The error branch converges at failed; revisit is expected for that
		// terminal and does not constitute a cycle.
		if !seen[*current.Error] {
			if err := visit(*current.Error); err != nil {
				return err
			}
		}
		delete(active, state)
		return nil
	}
	if err := visit(flow.Start); err != nil {
		return err
	}
	for state := range flow.States {
		if !seen[state] {
			return fmt.Errorf("selection flow: unreachable state %q", state)
		}
	}
	if !seen["committed"] || !seen["failed"] {
		return errors.New("selection flow: both terminal states must be reachable")
	}
	return nil
}
