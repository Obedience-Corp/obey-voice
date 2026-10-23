package config

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/decider"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/qwen"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/textclean"
)

// EnsureRuntimeAssetsProgress downloads any missing model files and archives
// needed for this run, reporting fine-grained per-asset progress (phase, byte
// counts, and a clamped percentage). The required asset set is resolved once
// from the asset manifest (ManifestFor), so URLs, file names, and extraction
// targets have a single source of truth; this function only performs the
// downloads. ctx cancels in-flight downloads (Ctrl-C during startup or render
// asset setup).
func EnsureRuntimeAssetsProgress(ctx context.Context, cfg *Config, req AssetRequest, onProgress func(AssetProgress)) error {
	// Throttled once, here, at the single point every AssetProgress tick
	// this function produces passes through — not just for the --json NDJSON
	// path, but for every consumer, including the legacy (name, pct) adapter
	// (EnsureRuntimeAssets) that TUI/CLI human-progress printers still use.
	// Without this, a download whose response has no Content-Length (Total
	// <= 0, so every tick's Pct is 0) fires onProgress once per 32KB chunk,
	// and callers like serveModelProgress/modelProgress treat Pct == 0 as a
	// "just started" sentinel that reprints "Downloading <name>..." on every
	// single tick instead of once.
	onProgress = throttleDownloadTicks(onProgress)

	manifest, err := ManifestFor(cfg, req)
	if err != nil {
		return err
	}
	dir := ModelsDirFrom(cfg)
	if err := ensureManifestProgress(ctx, manifest, dir, onProgress); err != nil {
		return err
	}
	if req.NeedDecision && ManagedDecisions(cfg) {
		if err := decider.EnsurePython(ctx, dir, func(stage string) {
			if onProgress != nil {
				onProgress(AssetProgress{Asset: "Local decision runtime", Phase: assetPhaseInstall})
			}
		}); err != nil {
			return err
		}
		if onProgress != nil {
			onProgress(AssetProgress{Asset: "Local decision runtime", Phase: assetPhaseDone, Pct: 100})
		}
	}
	if req.NeedTTS && cfg != nil && strings.EqualFold(strings.TrimSpace(cfg.TTSProvider), qwen.ProviderName) {
		if qwen.UseManaged(cfg.QwenTTSBinary, cfg.QwenTTSModel) {
			return ensureQwenTTSAssetsProgress(ctx, cfg, onProgress)
		}
		return nil
	}
	if req.NeedTTS && OpenAISpeechSelected(cfg) {
		return ensureOpenAISpeechAssetsProgress(ctx, cfg, onProgress)
	}
	if req.NeedTTS && cfg != nil && strings.EqualFold(strings.TrimSpace(cfg.TTSProvider), "kokoro") {
		if err := sanitizeKokoroLexicons(dir); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not sanitize kokoro lexicons: %v\n", err)
		}
		// Product TTS is the sherpa multi-lang pack already in the manifest.
		// Do not probe Python here: the optional Kokoro v1 English converter
		// is a lab import (EnsureKokoroV1EnglishPack), never serve startup.
	}
	return nil
}

// EnsureRuntimeAssets is the coarse (name, pct) form kept for the existing
// callers (speaker enroll, onboarding install, render/narrate paths): a thin
// adapter over EnsureRuntimeAssetsProgress so they see the same values as
// before without taking on the richer AssetProgress shape.
func EnsureRuntimeAssets(ctx context.Context, cfg *Config, req AssetRequest, onProgress func(name string, pct float64)) error {
	return EnsureRuntimeAssetsProgress(ctx, cfg, req, func(p AssetProgress) {
		if onProgress != nil {
			onProgress(p.Asset, p.Pct)
		}
	})
}

// ensureManifest is the legacy (name, pct) core, kept as a thin adapter over
// ensureManifestProgress for its direct callers/tests.
func ensureManifest(ctx context.Context, manifest AssetManifest, dir string, onProgress func(name string, pct float64)) error {
	return ensureManifestProgress(ctx, manifest, dir, func(p AssetProgress) {
		if onProgress != nil {
			onProgress(p.Asset, p.Pct)
		}
	})
}

// ensureManifestProgress downloads every missing file and archive in the
// manifest into dir, emitting one AssetProgress tick per phase transition.
// It is the parameterized core of EnsureRuntimeAssetsProgress, so it can be
// tested against a temp dir and a fake HTTP server. Already-present files and
// already-extracted archives are skipped (no re-download) but still emit a
// single "skipped" tick, so a no-op run still reports what it checked.
func ensureManifestProgress(ctx context.Context, manifest AssetManifest, dir string, onProgress func(AssetProgress)) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create models dir: %w", err)
	}

	emit := func(asset, phase string, bytes, total int64) {
		if onProgress != nil {
			onProgress(newAssetProgress(asset, phase, bytes, total))
		}
	}

	// Individual file downloads.
	for _, m := range manifest.ModelFiles() {
		if err := ctx.Err(); err != nil {
			return err
		}
		path := filepath.Join(dir, m.Name)
		if fileVerified(path, m.Size, m.SHA256) {
			emit(m.Name, assetPhaseSkipped, 0, 0)
			continue
		}

		emit(m.Name, assetPhaseStart, 0, 0)

		var lastBytes, lastTotal int64
		if err := downloadFile(ctx, path, m.URL, m.Name, m.Size, m.SHA256, func(bytes, total int64) {
			lastBytes, lastTotal = bytes, total
			emit(m.Name, assetPhaseDownload, bytes, total)
		}); err != nil {
			return err
		}
		// downloadFile only returns once copyBodyVerified's checksum/size
		// check has already passed, so verify is reported after the fact.
		emit(m.Name, assetPhaseVerify, lastBytes, lastTotal)
		emit(m.Name, assetPhaseDone, lastBytes, lastTotal)
	}

	// Archive downloads with extraction.
	for _, a := range manifest.ModelArchives(dir) {
		if err := ctx.Err(); err != nil {
			return err
		}
		targetDir := dir
		if a.TargetDir != "" {
			targetDir = a.TargetDir
		}

		installed, err := ensureArchiveInstalled(targetDir, a.ID, a.URL, a.SHA256, a.CheckFiles)
		if err != nil {
			return err
		}
		if installed {
			emit(a.Name, assetPhaseSkipped, 0, 0)
			continue
		}

		emit(a.Name, assetPhaseStart, 0, 0)

		var lastBytes, lastTotal int64
		if err := downloadAndExtractArchive(ctx, targetDir, a.ID, a.URL, a.Name, a.CheckFiles, a.SHA256, func(bytes, total int64) {
			lastBytes, lastTotal = bytes, total
			emit(a.Name, assetPhaseDownload, bytes, total)
		}); err != nil {
			return err
		}
		// downloadAndExtractArchive verifies the checksum, extracts, and
		// promotes the result before returning, so verify/extract/install
		// are reported in order after the fact, same as verify above.
		emit(a.Name, assetPhaseVerify, lastBytes, lastTotal)
		emit(a.Name, assetPhaseExtract, lastBytes, lastTotal)
		emit(a.Name, assetPhaseInstall, lastBytes, lastTotal)
		emit(a.Name, assetPhaseDone, lastBytes, lastTotal)
	}

	return nil
}

// EnsureModels preserves the old behavior for callers that still need the default asset set.
func EnsureModels(ctx context.Context, cfg *Config, onProgress func(name string, pct float64)) error {
	return EnsureRuntimeAssets(ctx, cfg, DefaultAssetRequest(cfg), onProgress)
}

func ensureArchiveInstalled(dir, id, url, sha256Hex string, checkFiles []string) (bool, error) {
	if !archiveExtracted(dir, checkFiles) {
		return false, nil
	}
	if sha256Hex == "" {
		return true, nil
	}
	if archiveInstallMarkerValid(dir, id, url, sha256Hex, checkFiles) {
		return true, nil
	}
	if archiveInstallMarkerExists(dir, id) {
		return false, nil
	}
	if err := writeArchiveInstallMarker(dir, id, url, sha256Hex, checkFiles); err != nil {
		return false, fmt.Errorf("adopt extracted archive %s: %w", id, err)
	}
	return true, nil
}

func sanitizeKokoroLexicons(dir string) error {
	paths, err := filepath.Glob(filepath.Join(dir, "lexicon*.txt"))
	if err != nil {
		return err
	}

	for _, path := range paths {
		if err := sanitizeKokoroLexicon(path); err != nil {
			return err
		}
	}

	return nil
}

func sanitizeKokoroLexicon(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	cleaned := textclean.StripUnsupportedKokoroMarks(string(data))
	if cleaned == string(data) {
		return nil
	}

	return os.WriteFile(path, []byte(cleaned), info.Mode().Perm())
}
