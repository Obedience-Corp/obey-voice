package config

import (
	"errors"
	"fmt"
	"math"
	"reflect"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type WorkerMode string

const (
	WorkerModeLegacy WorkerMode = "legacy"
	WorkerModeTiered WorkerMode = "tiered"
)

type WorkerTierName string

const (
	WorkerTierSimple  WorkerTierName = "simple"
	WorkerTierMedium  WorkerTierName = "medium"
	WorkerTierComplex WorkerTierName = "complex"
)

type WorkerBilling string

const (
	WorkerBillingLocal        WorkerBilling = "local"
	WorkerBillingSubscription WorkerBilling = "subscription"
	WorkerBillingMetered      WorkerBilling = "metered"
	WorkerBillingUnknown      WorkerBilling = "unknown"
)

// WorkerAssignment contains references only; credentials are never persisted here.
type WorkerAssignment struct {
	Provider    string        `mapstructure:"provider" yaml:"provider" json:"provider"`
	Model       string        `mapstructure:"model" yaml:"model" json:"model"`
	EndpointRef *string       `mapstructure:"endpoint_ref" yaml:"endpoint_ref" json:"endpoint_ref"`
	AccountRef  *string       `mapstructure:"account_ref" yaml:"account_ref" json:"account_ref"`
	Billing     WorkerBilling `mapstructure:"billing" yaml:"billing" json:"billing"`
}

type WorkerLimits struct {
	RuntimeSeconds   int    `mapstructure:"runtime_seconds" yaml:"runtime_seconds" json:"runtime_seconds"`
	ModelRequests    int    `mapstructure:"model_requests" yaml:"model_requests" json:"model_requests"`
	ToolCalls        int    `mapstructure:"tool_calls" yaml:"tool_calls" json:"tool_calls"`
	TransportRetries int    `mapstructure:"transport_retries" yaml:"transport_retries" json:"transport_retries"`
	OutputTokens     *int   `mapstructure:"output_tokens" yaml:"output_tokens" json:"output_tokens"`
	ChargeMicroUSD   *int64 `mapstructure:"charge_micro_usd" yaml:"charge_micro_usd" json:"charge_micro_usd"`
}

type WorkerTier struct {
	Enabled       bool              `mapstructure:"enabled" yaml:"enabled" json:"enabled"`
	Revision      int64             `mapstructure:"revision" yaml:"revision" json:"revision"`
	Assignment    *WorkerAssignment `mapstructure:"assignment" yaml:"assignment" json:"assignment"`
	Limits        WorkerLimits      `mapstructure:"limits" yaml:"limits" json:"limits"`
	AllowUncapped bool              `mapstructure:"allow_uncapped" yaml:"allow_uncapped" json:"allow_uncapped"`
}

type WorkerPolicy struct {
	PaidEnabled         bool   `mapstructure:"paid_enabled" yaml:"paid_enabled" json:"paid_enabled"`
	ChargePolicy        string `mapstructure:"charge_policy" yaml:"charge_policy" json:"charge_policy"`
	DailyChargeMicroUSD *int64 `mapstructure:"daily_charge_micro_usd" yaml:"daily_charge_micro_usd" json:"daily_charge_micro_usd"`
	Timezone            string `mapstructure:"timezone" yaml:"timezone" json:"timezone"`
	ConcurrentWorkers   int    `mapstructure:"concurrent_workers" yaml:"concurrent_workers" json:"concurrent_workers"`
	QueuedWorkers       int    `mapstructure:"queued_workers" yaml:"queued_workers" json:"queued_workers"`
	Escalation          string `mapstructure:"escalation" yaml:"escalation" json:"escalation"`
}

// VoiceWorkers is the versioned worker assignment and policy document.
type VoiceWorkers struct {
	SchemaVersion int        `mapstructure:"schema_version" yaml:"schema_version" json:"schema_version"`
	Revision      int64      `mapstructure:"revision" yaml:"revision" json:"revision"`
	Mode          WorkerMode `mapstructure:"mode" yaml:"mode" json:"mode"`
	Tiers         struct {
		Simple  WorkerTier `yaml:"simple" json:"simple"`
		Medium  WorkerTier `yaml:"medium" json:"medium"`
		Complex WorkerTier `yaml:"complex" json:"complex"`
	} `mapstructure:"tiers" yaml:"tiers" json:"tiers"`
	Policy WorkerPolicy `mapstructure:"policy" yaml:"policy" json:"policy"`
}

func defaultWorkerTier() WorkerTier {
	return WorkerTier{Limits: WorkerLimits{RuntimeSeconds: 120, ModelRequests: 10, ToolCalls: 20, TransportRetries: 1}}
}

// DefaultVoiceWorkers is safe for new installs: no tier is enabled or assigned.
func DefaultVoiceWorkers() *VoiceWorkers {
	w := &VoiceWorkers{SchemaVersion: 1, Mode: WorkerModeLegacy, Policy: WorkerPolicy{PaidEnabled: false, ChargePolicy: "advisory", Timezone: "UTC", ConcurrentWorkers: 1, QueuedWorkers: 1, Escalation: "never"}}
	w.Tiers.Simple, w.Tiers.Medium, w.Tiers.Complex = defaultWorkerTier(), defaultWorkerTier(), defaultWorkerTier()
	return w
}

func (w *VoiceWorkers) Clone() *VoiceWorkers {
	if w == nil {
		return nil
	}
	c := *w
	c.Tiers.Simple = cloneTier(w.Tiers.Simple)
	c.Tiers.Medium = cloneTier(w.Tiers.Medium)
	c.Tiers.Complex = cloneTier(w.Tiers.Complex)
	c.Policy.DailyChargeMicroUSD = cloneInt64(w.Policy.DailyChargeMicroUSD)
	return &c
}
func cloneInt64(v *int64) *int64 {
	if v == nil {
		return nil
	}
	n := *v
	return &n
}
func cloneTier(t WorkerTier) WorkerTier {
	c := t
	c.Limits.OutputTokens = cloneInt(t.Limits.OutputTokens)
	c.Limits.ChargeMicroUSD = cloneInt64(t.Limits.ChargeMicroUSD)
	if t.Assignment != nil {
		a := *t.Assignment
		a.EndpointRef = cloneString(a.EndpointRef)
		a.AccountRef = cloneString(a.AccountRef)
		c.Assignment = &a
	}
	return c
}
func cloneInt(v *int) *int {
	if v == nil {
		return nil
	}
	n := *v
	return &n
}
func cloneString(v *string) *string {
	if v == nil {
		return nil
	}
	n := *v
	return &n
}

func (w *VoiceWorkers) tier(name WorkerTierName) *WorkerTier {
	if w == nil {
		return nil
	}
	switch name {
	case WorkerTierSimple:
		return &w.Tiers.Simple
	case WorkerTierMedium:
		return &w.Tiers.Medium
	case WorkerTierComplex:
		return &w.Tiers.Complex
	}
	return nil
}

func (w *VoiceWorkers) Validate() error {
	if w == nil {
		return errors.New("voice_workers is missing")
	}
	if w.SchemaVersion != 1 {
		return fmt.Errorf("voice_workers schema_version must be 1")
	}
	if w.Revision < 0 {
		return errors.New("voice_workers revision must be non-negative")
	}
	if w.Mode != WorkerModeLegacy && w.Mode != WorkerModeTiered {
		return fmt.Errorf("invalid voice_workers mode %q", w.Mode)
	}
	if err := validateTier("simple", w.Tiers.Simple); err != nil {
		return err
	}
	if err := validateTier("medium", w.Tiers.Medium); err != nil {
		return err
	}
	if err := validateTier("complex", w.Tiers.Complex); err != nil {
		return err
	}
	p := w.Policy
	if p.ChargePolicy != "strict" && p.ChargePolicy != "advisory" {
		return fmt.Errorf("invalid worker charge_policy %q", p.ChargePolicy)
	}
	if p.ChargePolicy == "strict" && p.DailyChargeMicroUSD == nil {
		return errors.New("strict worker policy requires daily_charge_micro_usd")
	}
	if p.DailyChargeMicroUSD != nil && *p.DailyChargeMicroUSD < 1 {
		return errors.New("daily_charge_micro_usd must be positive")
	}
	if strings.TrimSpace(p.Timezone) == "" || p.ConcurrentWorkers < 1 || p.ConcurrentWorkers > 32 || p.QueuedWorkers < 1 || p.QueuedWorkers > 32 || p.Escalation != "never" {
		return errors.New("invalid worker policy limits or escalation")
	}
	if _, err := time.LoadLocation(p.Timezone); err != nil {
		return fmt.Errorf("invalid worker policy timezone %q", p.Timezone)
	}
	return nil
}

// validateWorkerYAML enforces the contract's required keys and rejects extra
// keys before mapstructure can silently discard them.
func validateWorkerYAML(node *yaml.Node) error {
	return validateTypedWorkerNode(node, reflect.TypeOf(VoiceWorkers{}), "voice_workers")
}

func validateTypedWorkerNode(n *yaml.Node, typ reflect.Type, path string) error {
	if n == nil {
		return fmt.Errorf("%s is missing", path)
	}
	if n.Tag == "!!null" {
		if typ.Kind() == reflect.Pointer {
			return nil
		}
		return fmt.Errorf("%s cannot be null", path)
	}
	if typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ.Kind() == reflect.Struct {
		if n.Kind != yaml.MappingNode {
			return fmt.Errorf("%s must be an object", path)
		}
		allowed := map[string]reflect.StructField{}
		for i := 0; i < typ.NumField(); i++ {
			f := typ.Field(i)
			name := strings.Split(f.Tag.Get("yaml"), ",")[0]
			allowed[name] = f
		}
		seen := map[string]bool{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i].Value
			f, ok := allowed[key]
			if !ok {
				return fmt.Errorf("unknown %s field %q", path, key)
			}
			if seen[key] {
				return fmt.Errorf("duplicate %s field %q", path, key)
			}
			seen[key] = true
			if err := validateTypedWorkerNode(n.Content[i+1], f.Type, path+"."+key); err != nil {
				return err
			}
		}
		for key := range allowed {
			if !seen[key] {
				return fmt.Errorf("%s missing required field %q", path, key)
			}
		}
		return nil
	}
	switch typ.Kind() {
	case reflect.Bool:
		if n.Tag != "!!bool" {
			return fmt.Errorf("%s must be boolean", path)
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if n.Tag != "!!int" {
			return fmt.Errorf("%s must be integer", path)
		}
	case reflect.String:
		if n.Tag != "!!str" {
			return fmt.Errorf("%s must be string", path)
		}
	}
	return nil
}
func validateTier(name string, t WorkerTier) error {
	if t.Revision < 0 {
		return fmt.Errorf("%s tier revision must be non-negative", name)
	}
	l := t.Limits
	if l.RuntimeSeconds < 1 || l.ModelRequests < 1 || l.ToolCalls < 1 || l.TransportRetries < 0 {
		return fmt.Errorf("invalid %s tier limits", name)
	}
	if l.OutputTokens != nil && *l.OutputTokens < 1 {
		return fmt.Errorf("%s output_tokens must be positive", name)
	}
	if l.ChargeMicroUSD != nil && *l.ChargeMicroUSD < 1 {
		return fmt.Errorf("%s charge_micro_usd must be positive", name)
	}
	if t.Enabled && (t.Assignment == nil || strings.TrimSpace(t.Assignment.Provider) == "" || strings.TrimSpace(t.Assignment.Model) == "" || !validBilling(t.Assignment.Billing)) {
		return fmt.Errorf("enabled %s tier requires a complete assignment", name)
	}
	if t.Assignment != nil && (!validBilling(t.Assignment.Billing) || strings.TrimSpace(t.Assignment.Provider) == "" || strings.TrimSpace(t.Assignment.Model) == "") {
		return fmt.Errorf("invalid %s assignment", name)
	}
	if a := t.Assignment; a != nil {
		if a.EndpointRef != nil && strings.TrimSpace(*a.EndpointRef) == "" {
			return fmt.Errorf("%s endpoint_ref must be null or non-empty", name)
		}
		if a.AccountRef != nil && strings.TrimSpace(*a.AccountRef) == "" {
			return fmt.Errorf("%s account_ref must be null or non-empty", name)
		}
	}
	return nil
}
func validBilling(b WorkerBilling) bool {
	return b == WorkerBillingLocal || b == WorkerBillingSubscription || b == WorkerBillingMetered || b == WorkerBillingUnknown
}

// WorkerCapabilityResolver is supplied by the provider layer. Config never
// infers support from names or billing labels.
type WorkerCapabilityResolver interface {
	ValidateWorkerAssignment(WorkerAssignment) error
}
type WorkerPlan struct {
	Tier           WorkerTierName
	ConfigRevision int64
	Revision       int64
	Assignment     WorkerAssignment
	Limits         WorkerLimits
	Policy         WorkerPolicy
	AllowUncapped  bool
}

func ResolveWorkerPlan(cfg *Config, name WorkerTierName, capabilities WorkerCapabilityResolver) (*WorkerPlan, error) {
	if cfg == nil {
		return nil, errors.New("nil config")
	}
	if cfg.VoiceWorkers == nil {
		return nil, errors.New("worker tiers are in legacy mode")
	}
	if cfg.VoiceWorkers.Mode != WorkerModeTiered {
		return nil, errors.New("worker tiers are not enabled")
	}
	if err := cfg.VoiceWorkers.Validate(); err != nil {
		return nil, err
	}
	t := cfg.VoiceWorkers.tier(name)
	if t == nil {
		return nil, fmt.Errorf("unknown worker tier %q", name)
	}
	if !t.Enabled || t.Assignment == nil {
		return nil, fmt.Errorf("worker tier %s is disabled", name)
	}
	// usage_policy has not yet qualified chargeable adapters. Keep every paid
	// assignment editable and visible, but refuse to produce a runnable plan.
	if t.Assignment.Billing == WorkerBillingUnknown {
		return nil, fmt.Errorf("worker tier %s needs an explicit billing label", name)
	}
	if t.Assignment.Billing != WorkerBillingLocal {
		return nil, errors.New("paid worker dispatch is unavailable until usage policy is enforced")
	}
	if capabilities == nil {
		return nil, errors.New("worker capabilities were not supplied")
	}
	if err := capabilities.ValidateWorkerAssignment(*t.Assignment); err != nil {
		return nil, fmt.Errorf("worker assignment is unavailable: %w", err)
	}
	clonedTier := cloneTier(*t)
	policy := cfg.VoiceWorkers.Policy
	policy.DailyChargeMicroUSD = cloneInt64(policy.DailyChargeMicroUSD)
	p := &WorkerPlan{Tier: name, ConfigRevision: cfg.VoiceWorkers.Revision, Revision: clonedTier.Revision, Assignment: *clonedTier.Assignment, Limits: clonedTier.Limits, Policy: policy, AllowUncapped: clonedTier.AllowUncapped}
	p.Assignment.EndpointRef = cloneString(p.Assignment.EndpointRef)
	p.Assignment.AccountRef = cloneString(p.Assignment.AccountRef)
	return p, nil
}

// PreviewLegacyWorkerMigration constructs an explicit all-tier migration but
// never writes it. It fails when the legacy worker has no concrete model.
func PreviewLegacyWorkerMigration(cfg *Config) (*VoiceWorkers, error) {
	if cfg == nil {
		return nil, errors.New("nil config")
	}
	effective := WorkerBrainConfig(cfg)
	provider := effective.BrainProvider
	model := BrainModelFor(effective, provider)
	if model == "" {
		model = effective.VoiceWorkerModel
	}
	if strings.TrimSpace(model) == "" {
		return nil, errors.New("legacy worker has no concrete model")
	}
	a := WorkerAssignment{Provider: provider, Model: model, Billing: WorkerBillingUnknown}
	if strings.TrimSpace(effective.VoiceWorkerOllamaHost) != "" || provider == "ollama" {
		host := cfg.VoiceWorkerOllamaHost
		if host == "" {
			host = cfg.OllamaHost
		}
		a.EndpointRef = &host
	}
	w := DefaultVoiceWorkers()
	w.Mode = WorkerModeTiered
	if cfg.VoiceWorkers != nil {
		w.Revision = cfg.VoiceWorkers.Revision
		w.Policy = cfg.VoiceWorkers.Policy
		w.Policy.DailyChargeMicroUSD = cloneInt64(cfg.VoiceWorkers.Policy.DailyChargeMicroUSD)
	}
	for _, t := range []*WorkerTier{&w.Tiers.Simple, &w.Tiers.Medium, &w.Tiers.Complex} {
		t.Enabled = true
		assignment := a
		assignment.EndpointRef = cloneString(a.EndpointRef)
		assignment.AccountRef = cloneString(a.AccountRef)
		t.Assignment = &assignment
	}
	return w, nil
}

var ErrWorkerRevisionConflict = errors.New("voice_workers revision conflict")

// CommitVoiceWorkersCAS validates and atomically replaces the complete object.
// expectedRevision is compared with the file on every call, including calls
// from another process, so unrelated key edits are retained by the read/patch.
func CommitVoiceWorkersCAS(workers *VoiceWorkers, expectedRevision int64) (*VoiceWorkers, error) {
	if expectedRevision < 0 {
		return nil, errors.New("expected worker revision must be non-negative")
	}
	if workers == nil {
		return nil, errors.New("voice_workers is nil")
	}
	if err := workers.Validate(); err != nil {
		return nil, err
	}
	fileWriteMu.Lock()
	defer fileWriteMu.Unlock()
	release, err := openConfigForWrite(ConfigFile(), "voice_workers")
	if err != nil {
		return nil, err
	}
	defer release()
	data, existed, doc, err := readConfigDocument(ConfigFile(), "voice_workers")
	if err != nil {
		return nil, err
	}
	if node := mappingValue(doc.Content[0], "voice_workers"); node != nil {
		if err := validateWorkerYAML(node); err != nil {
			return nil, err
		}
	}
	var current int64
	var currentWorkers VoiceWorkers
	if node := mappingValue(doc.Content[0], "voice_workers"); node != nil {
		if err := node.Decode(&currentWorkers); err != nil {
			return nil, &SetError{Code: CodeParseFailed, Key: "voice_workers", Message: "voice_workers has invalid shape", cause: err}
		}
		if err := currentWorkers.Validate(); err != nil {
			return nil, fmt.Errorf("invalid persisted worker settings: %w", err)
		}
	}
	if raw, ok := yamlValueAt(doc.Content[0], []string{"voice_workers", "revision"}); ok {
		switch n := raw.(type) {
		case int:
			current = int64(n)
		case int64:
			current = n
		case uint64:
			if n > uint64(math.MaxInt64) {
				return nil, errors.New("voice_workers revision overflow")
			}
			current = int64(n)
		default:
			return nil, &SetError{Code: CodeParseFailed, Key: "voice_workers", Message: "voice_workers revision is not an integer"}
		}
	}
	if current != expectedRevision {
		return nil, fmt.Errorf("%w: expected %d, found %d", ErrWorkerRevisionConflict, expectedRevision, current)
	}
	if expectedRevision == math.MaxInt64 {
		return nil, errors.New("voice_workers revision overflow")
	}
	copy := workers.Clone()
	copy.Revision = expectedRevision + 1
	var revErr error
	copy.Tiers.Simple.Revision, revErr = nextTierRevision(currentWorkers.Tiers.Simple, copy.Tiers.Simple)
	if revErr != nil {
		return nil, revErr
	}
	copy.Tiers.Medium.Revision, revErr = nextTierRevision(currentWorkers.Tiers.Medium, copy.Tiers.Medium)
	if revErr != nil {
		return nil, revErr
	}
	copy.Tiers.Complex.Revision, revErr = nextTierRevision(currentWorkers.Tiers.Complex, copy.Tiers.Complex)
	if revErr != nil {
		return nil, revErr
	}
	patched, err := patchConfigSource(data, doc, []string{"voice_workers"}, copy)
	if err != nil {
		return nil, writeOrShapeFailed("voice_workers", "updating config", err)
	}
	if _, err := verifyPatched(patched, []string{"voice_workers", "revision"}, copy.Revision); err != nil {
		return nil, writeFailed("voice_workers", "updating config", err)
	}
	if _, err := backupAndReplace(ConfigFile(), patched, existed, "voice_workers"); err != nil {
		return nil, err
	}
	return copy, nil
}

func SaveVoiceWorkersCAS(workers *VoiceWorkers, expectedRevision int64) error {
	_, err := CommitVoiceWorkersCAS(workers, expectedRevision)
	return err
}

func nextTierRevision(old, next WorkerTier) (int64, error) {
	oldCopy, nextCopy := old, next
	oldCopy.Revision, nextCopy.Revision = 0, 0
	if sameEncoded(oldCopy, nextCopy) {
		return old.Revision, nil
	}
	if old.Revision == math.MaxInt64 {
		return 0, errors.New("worker tier revision overflow")
	}
	return old.Revision + 1, nil
}

func SaveVoiceWorkers(workers *VoiceWorkers) error {
	if workers == nil {
		return errors.New("voice_workers is nil")
	}
	return SaveVoiceWorkersCAS(workers, workers.Revision)
}
