package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"gopkg.in/yaml.v3"
)

// WorkerTierOverrides belongs to one persona. Installation policy is never
// overridable here. Omitted tiers and fields inherit installation defaults.
type WorkerTierOverrides struct {
	Simple  *WorkerTierOverride `yaml:"simple,omitempty" json:"simple,omitempty"`
	Medium  *WorkerTierOverride `yaml:"medium,omitempty" json:"medium,omitempty"`
	Complex *WorkerTierOverride `yaml:"complex,omitempty" json:"complex,omitempty"`
}

// Assignment and Limits replace their complete groups; null means inherit.
// Pointer booleans distinguish inheritance from an explicit false.
type WorkerTierOverride struct {
	Enabled       *bool             `yaml:"enabled,omitempty" json:"enabled,omitempty"`
	Assignment    *WorkerAssignment `yaml:"assignment,omitempty" json:"assignment,omitempty"`
	Limits        *WorkerLimits     `yaml:"limits,omitempty" json:"limits,omitempty"`
	AllowUncapped *bool             `yaml:"allow_uncapped,omitempty" json:"allow_uncapped,omitempty"`
}

func (o *WorkerTierOverrides) Clone() *WorkerTierOverrides {
	if o == nil {
		return nil
	}
	clone := func(p *WorkerTierOverride) *WorkerTierOverride {
		if p == nil {
			return nil
		}
		c := *p
		if p.Enabled != nil {
			b := *p.Enabled
			c.Enabled = &b
		}
		if p.AllowUncapped != nil {
			b := *p.AllowUncapped
			c.AllowUncapped = &b
		}
		t := cloneTier(WorkerTier{Assignment: p.Assignment})
		c.Assignment = t.Assignment
		if p.Limits != nil {
			l := cloneTier(WorkerTier{Limits: *p.Limits}).Limits
			c.Limits = &l
		}
		return &c
	}
	return &WorkerTierOverrides{Simple: clone(o.Simple), Medium: clone(o.Medium), Complex: clone(o.Complex)}
}

func applyTierOverride(t *WorkerTier, p *WorkerTierOverride) {
	if p == nil {
		return
	}
	if p.Enabled != nil {
		t.Enabled = *p.Enabled
	}
	if p.Assignment != nil {
		t.Assignment = cloneTier(WorkerTier{Assignment: p.Assignment}).Assignment
	}
	if p.Limits != nil {
		t.Limits = cloneTier(WorkerTier{Limits: *p.Limits}).Limits
	}
	if p.AllowUncapped != nil {
		t.AllowUncapped = *p.AllowUncapped
	}
}

// Validate checks supplied groups without requiring inherited assignments.
func (o *WorkerTierOverrides) Validate() error {
	if o == nil {
		return nil
	}
	for name, p := range map[string]*WorkerTierOverride{"simple": o.Simple, "medium": o.Medium, "complex": o.Complex} {
		if p == nil {
			continue
		}
		tier := defaultWorkerTier()
		applyTierOverride(&tier, p)
		tier.Enabled = false
		if err := validateTier(name, tier); err != nil {
			return fmt.Errorf("persona worker_tiers: %w", err)
		}
	}
	return nil
}

// EffectiveVoiceWorkers resolves a fresh admission snapshot. VoiceWorkers stays
// installation-owned so settings saves and paid-policy checks remain global.
func (cfg *Config) EffectiveVoiceWorkers() (*VoiceWorkers, error) {
	if cfg == nil || cfg.VoiceWorkers == nil {
		return nil, nil
	}
	w := cfg.VoiceWorkers.Clone()
	if w.Mode == WorkerModeTiered && cfg.PersonaWorkerTiers != nil {
		if err := cfg.PersonaWorkerTiers.Validate(); err != nil {
			return nil, err
		}
		applyTierOverride(&w.Tiers.Simple, cfg.PersonaWorkerTiers.Simple)
		applyTierOverride(&w.Tiers.Medium, cfg.PersonaWorkerTiers.Medium)
		applyTierOverride(&w.Tiers.Complex, cfg.PersonaWorkerTiers.Complex)
	}
	if err := w.Validate(); err != nil {
		return nil, err
	}
	return w, nil
}

// Reject unsupported policy keys instead of silently accepting a persona file
// that appears to override installation-wide controls.
func (o *WorkerTierOverrides) UnmarshalYAML(node *yaml.Node) error {
	type plain WorkerTierOverrides
	var check func(*yaml.Node, reflect.Type) error
	check = func(n *yaml.Node, t reflect.Type) error {
		if n.Tag == "!!null" {
			return nil
		}
		if t.Kind() == reflect.Pointer {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct {
			return nil
		}
		if n.Kind != yaml.MappingNode {
			return fmt.Errorf("worker_tiers must contain objects")
		}
		fields := map[string]reflect.Type{}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			fields[strings.Split(f.Tag.Get("yaml"), ",")[0]] = f.Type
		}
		for i := 0; i < len(n.Content); i += 2 {
			name := n.Content[i].Value
			typ, ok := fields[name]
			if !ok {
				return fmt.Errorf("unknown persona worker_tiers field %q", name)
			}
			if err := check(n.Content[i+1], typ); err != nil {
				return err
			}
		}
		return nil
	}
	if err := check(node, reflect.TypeOf(plain{})); err != nil {
		return err
	}
	var decoded plain
	if err := node.Decode(&decoded); err != nil {
		return err
	}
	*o = WorkerTierOverrides(decoded)
	return o.Validate()
}

// HasOverrides distinguishes an inherited configuration from empty YAML objects.
func (o *WorkerTierOverrides) HasOverrides() bool {
	if o == nil {
		return false
	}
	for _, p := range []*WorkerTierOverride{o.Simple, o.Medium, o.Complex} {
		if p != nil && (p.Enabled != nil || p.Assignment != nil || p.Limits != nil || p.AllowUncapped != nil) {
			return true
		}
	}
	return false
}

func (o *WorkerTierOverrides) UnmarshalJSON(data []byte) error {
	type plain WorkerTierOverrides
	var decoded plain
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*o = WorkerTierOverrides(decoded)
	return o.Validate()
}
