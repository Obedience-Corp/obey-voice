package config

import (
	"time"
)

// ModelFile describes a required model file (individual download).
type ModelFile struct {
	Name   string
	URL    string
	Size   int64  // expected size in bytes (0 = skip check)
	SHA256 string // expected hex checksum (empty = skip check)
}

// ModelArchive describes a tar.bz2 archive to download and extract.
type ModelArchive struct {
	ID         string   // stable manifest asset id
	Name       string   // display name for progress
	URL        string   // tar.bz2 URL
	SHA256     string   // expected hex checksum of compressed archive (empty = skip check)
	TargetDir  string   // extraction target directory (defaults to ModelsDir)
	CheckFiles []string // paths relative to TargetDir to verify extraction
}

// AssetRequest describes which runtime assets are needed for a command.
type AssetRequest struct {
	NeedDecision bool
	NeedSTT      bool
	NeedTTS      bool
	NeedVAD      bool
	NeedSpeaker  bool
	// NeedVoiceCatalog verifies/refreshes Phonon's named voices and fetches
	// descriptive catalogs (voices.jsonl). Only an explicit install asks: the catalog never gates
	// speech, so starting a conversation, listing voices or a preview must not
	// wait on its host.
	NeedVoiceCatalog bool
}

// AssetProgress is one progress tick for a single asset, reported by
// EnsureRuntimeAssetsProgress. Bytes/Total are 0 when unknown (no
// Content-Length, or a phase that has no byte count of its own); Pct is
// always clamped to [0,100] regardless of how the emitting phase computed it.
type AssetProgress struct {
	Asset string  `json:"asset"`
	Phase string  `json:"phase"` // start|download|verify|extract|install|done|skipped
	Bytes int64   `json:"bytes"`
	Total int64   `json:"total"`
	Pct   float64 `json:"pct"`
	// Detail says why an asset closed as skipped without being installed.
	Detail string `json:"detail,omitempty"`
}

const (
	assetPhaseStart    = "start"
	assetPhaseDownload = "download"
	assetPhaseVerify   = "verify"
	assetPhaseExtract  = "extract"
	assetPhaseInstall  = "install"
	assetPhaseDone     = "done"
	assetPhaseSkipped  = "skipped"
)

// clampPct is the single place asset progress percentages are bounded to
// [0,100]. Every emitter (manifest downloads, qwen native installs, the
// already-installed shortcuts) routes its percentage through this or
// through newAssetProgress, so a bad Content-Length or an over-eager caller
// can never produce an out-of-range tick.
func clampPct(pct float64) float64 {
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

// pctOfBytes computes a clamped percentage from a byte count and a total.
// It returns 0 when total is unknown (<=0).
func pctOfBytes(bytes, total int64) float64 {
	if total <= 0 {
		return 0
	}
	return clampPct(float64(bytes) / float64(total) * 100)
}

// newAssetProgress builds an AssetProgress tick, deriving Pct from bytes/total
// and forcing it to 100 for the done/skipped phases (which report completion
// even when total is unknown). A negative total (net/http's convention for
// "no Content-Length") is normalised to 0 here, so Total is always either the
// real byte count or 0 — never a sentinel.
func newAssetProgress(asset, phase string, bytes, total int64) AssetProgress {
	if total < 0 {
		total = 0
	}
	pct := pctOfBytes(bytes, total)
	if phase == assetPhaseDone || phase == assetPhaseSkipped {
		pct = 100
	}
	return AssetProgress{Asset: asset, Phase: phase, Bytes: bytes, Total: total, Pct: pct}
}

// throttleDownloadTicks wraps onProgress so "download"-phase ticks are
// forwarded only when Pct has advanced at least one point or 250ms have
// passed since the last tick emitted for that asset. Every other phase
// (start/verify/extract/install/done/skipped) is always forwarded, which
// already guarantees the first and last tick per asset are never dropped —
// those phases bookend every asset's download ticks. A nil onProgress
// passes through as nil so callers keep their existing "no callback" fast
// path.
//
// This is the single place per-chunk download ticks are rate-limited before
// reaching any consumer: both EnsureRuntimeAssetsProgress's callers (the
// --json NDJSON path and the legacy (name, pct) adapter) get a throttled
// stream, never a raw per-32KB-chunk flood — that matters most exactly when
// Total is unknown (no Content-Length), where every tick's Pct is 0 and a
// naive forward would fire once per chunk indefinitely.
func throttleDownloadTicks(onProgress func(AssetProgress)) func(AssetProgress) {
	if onProgress == nil {
		return nil
	}
	type tickState struct {
		pct float64
		at  time.Time
	}
	last := map[string]tickState{}
	return func(p AssetProgress) {
		if p.Phase != assetPhaseDownload {
			delete(last, p.Asset) // the asset's next download tick always fires
			onProgress(p)
			return
		}
		prev, seen := last[p.Asset]
		if !seen || p.Pct-prev.pct >= 1 || time.Since(prev.at) >= 250*time.Millisecond {
			last[p.Asset] = tickState{pct: p.Pct, at: time.Now()}
			onProgress(p)
		}
	}
}
