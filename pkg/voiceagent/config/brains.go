package config

import (
	"slices"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// BrainProviderSpec is one implemented brain backend. Each backend registers
// itself so the schema enum, doctor, and Settings never keep a parallel list
// of names.
type BrainProviderSpec struct {
	Name        string
	Description string
	// CLIBinary is the PATH name doctor looks for. Empty means the provider is
	// not a CLI (today: ollama).
	CLIBinary string
}

var (
	brainMu        sync.Mutex
	brainProviders []BrainProviderSpec
)

// RegisterBrainProvider records an implemented brain backend. Called from
// brain package init; later duplicates of the same name are ignored.
func RegisterBrainProvider(spec BrainProviderSpec) {
	name := strings.TrimSpace(strings.ToLower(spec.Name))
	if name == "" {
		return
	}
	spec.Name = name
	brainMu.Lock()
	defer brainMu.Unlock()
	for _, existing := range brainProviders {
		if existing.Name == name {
			return
		}
	}
	brainProviders = append(brainProviders, spec)
}

// RegisteredBrainProviders returns the backends that have registered, in
// registration order.
func RegisteredBrainProviders() []BrainProviderSpec {
	brainMu.Lock()
	defer brainMu.Unlock()
	return slices.Clone(brainProviders)
}

func lookupBrainProvider(name string) (BrainProviderSpec, bool) {
	name = strings.TrimSpace(strings.ToLower(name))
	for _, spec := range RegisteredBrainProviders() {
		if spec.Name == name {
			return spec, true
		}
	}
	return BrainProviderSpec{}, false
}

func brainProviderEnum() []string {
	specs := RegisteredBrainProviders()
	names := make([]string, 0, len(specs))
	for _, spec := range specs {
		names = append(names, spec.Name)
	}
	slices.Sort(names)
	return names
}

func workerProviderEnum() []string {
	return slices.Clone(WorkerProviders)
}

func brainProviderHelp() string {
	var local, clis []string
	for _, spec := range RegisteredBrainProviders() {
		label := titleBrainName(spec.Name)
		if spec.CLIBinary == "" {
			local = append(local, label)
			continue
		}
		clis = append(clis, label)
	}
	slices.Sort(local)
	slices.Sort(clis)
	localClause := joinEnglish(local, "and") + " " + pluralVerb(len(local), "runs", "run") + " on this Mac"
	cliClause := joinEnglish(clis, "and") + " " + pluralVerb(len(clis), "calls out to its CLI", "call out to their CLIs")
	switch {
	case len(local) > 0 && len(clis) > 0:
		return "Who answers. " + localClause + "; " + cliClause + "."
	case len(clis) > 0:
		return "Who answers. " + cliClause + "."
	case len(local) > 0:
		return "Who answers. " + localClause + "."
	default:
		return "Who answers."
	}
}

func pluralVerb(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

func titleBrainName(name string) string {
	r, size := utf8.DecodeRuneInString(name)
	if r == utf8.RuneError {
		return name
	}
	return string(unicode.ToUpper(r)) + name[size:]
}

func joinEnglish(items []string, conj string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " " + conj + " " + items[1]
	default:
		return strings.Join(items[:len(items)-1], ", ") + ", " + conj + " " + items[len(items)-1]
	}
}
