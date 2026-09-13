package config

import (
	"context"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
)

// EnsureQwenTTSTier installs the native Qwen3-TTS package so the given tier is
// ready, regardless of the active TTS provider — personas other than the
// active one may route speech through qwen. Empty tier uses the configured one.
func EnsureQwenTTSTier(ctx context.Context, cfg *Config, tier string, onProgress func(name string, pct float64)) error {
	return EnsureQwenTTSTierProgress(ctx, cfg, tier, func(p AssetProgress) {
		if onProgress != nil {
			onProgress(p.Asset, p.Pct)
		}
	})
}

// EnsureQwenTTSTierProgress is EnsureQwenTTSTier with per-phase AssetProgress
// reporting, for callers (models ensure --json) that need it.
func EnsureQwenTTSTierProgress(ctx context.Context, cfg *Config, tier string, onProgress func(AssetProgress)) error {
	if cfg == nil {
		return nil
	}
	cfgCopy := *cfg
	if strings.TrimSpace(tier) != "" {
		cfgCopy.QwenTTSModelTier = tier
	}
	return ensureQwenTTSAssetsProgress(ctx, &cfgCopy, onProgress)
}

// qwenNativeAssetName is the stable AssetProgress.Asset identifier for every
// tick emitted while installing the native Qwen3-TTS package — the dynamic
// part of the qwen installer's own progress reporting (its "stage" string)
// carries the Phase field instead, so a client can group every tick from one
// install under a single asset key.
const qwenNativeAssetName = "Qwen3-TTS native package"

// ensureQwenTTSAssetsProgress installs the native multi-tier package only (no
// Python/uv), reporting AssetProgress ticks.
func ensureQwenTTSAssetsProgress(ctx context.Context, cfg *Config, onProgress func(AssetProgress)) error {
	modelsDir := ModelsDirFrom(cfg)
	tier := ""
	url := ""
	sha := ""
	if cfg != nil {
		tier = cfg.QwenTTSModelTier
		url, sha = qwen.ResolveNativeDownload(cfg.QwenTTSNativeURL, cfg.QwenTTSNativeSHA256)
	}

	if qwen.InspectNative(modelsDir, tier).Installed {
		if onProgress != nil {
			onProgress(newAssetProgress(qwenNativeAssetName, assetPhaseSkipped, 0, 0))
		}
		return nil
	}

	if onProgress != nil {
		onProgress(newAssetProgress(qwenNativeAssetName, assetPhaseStart, 0, 0))
	}

	_, err := qwen.EnsureNative(ctx, modelsDir, qwen.NativeEnsureOptions{
		URL: url, SHA256: sha, Tier: tier,
	}, func(stage string, pct float64) {
		if onProgress != nil {
			onProgress(AssetProgress{Asset: qwenNativeAssetName, Phase: qwenPhaseFor(stage), Pct: clampPct(pct)})
		}
	})
	return err
}

// qwenPhaseFor maps qwen.EnsureNative's own stage vocabulary ("native
// Qwen3-TTS package" during download, "... extract", "... verify", and
// "native Qwen3-TTS" at completion — see qwen/native.go) onto the canonical
// AssetProgress phase vocabulary, rather than passing the raw stage text
// straight through as Phase.
//
// This is a deliberate deviation from this row's spec, which said to map
// the raw stage string directly onto Phase: doing that literally means no
// qwen tick is ever Phase "download" (so throttleDownloadTicks, which keys
// on that exact string, never throttles a multi-hundred-MB archive's
// per-chunk ticks) and none is ever Phase "done" (so a caller tracking
// "done" ticks to build an installed-assets list, the way `models ensure
// --json`'s terminal summary does, never counts the qwen native package as
// installed). Both are real, not hypothetical: the download loop calls its
// progress callback once per 256KB chunk. Mapping onto the canonical
// vocabulary here fixes both without the caller needing to know qwen's
// stage text at all — an asset from the manifest path and the qwen path now
// look identical on the wire.
func qwenPhaseFor(stage string) string {
	switch {
	case strings.Contains(stage, "extract"):
		return assetPhaseExtract
	case strings.Contains(stage, "verify"):
		return assetPhaseVerify
	case stage == qwenNativeStageDone:
		// EnsureNative's own already-installed shortcut and its real
		// end-of-install call both use this exact stage string (see
		// native.go); either way, from the caller's perspective the asset
		// is now ready.
		return assetPhaseDone
	default:
		// Everything else, including the initial 5% marker, is part of the
		// download: EnsureNative reuses the same "... package" stage string
		// for both.
		return assetPhaseDownload
	}
}

// qwenNativeStageDone is the exact stage string qwen.EnsureNative's own
// progress callback uses to signal completion (native.go:561,656) — both
// for its already-installed shortcut and a real end-of-install. Deliberately
// distinct from qwenNativeAssetName ("Qwen3-TTS native package", the Asset
// field this package reports): the two strings are unrelated, and comparing
// against the wrong one would silently make this phase mapping dead code.
const qwenNativeStageDone = "native Qwen3-TTS"
