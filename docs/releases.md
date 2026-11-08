# Native releases

`just release linux-artifact` and `just release macos-artifact` build on native
x86_64 glibc Linux and Apple Silicon macOS, respectively. Use Python 3.12+, Bash
4+, Go and the native toolchain; Linux also needs `patchelf` and binutils. CI uses
Ubuntu 24.04 and macOS 15. These tools are required only on the builder.

Each build creates `dist/release/obey-voice-VERSION-PLATFORM.tar.gz` and a matching
`.tar.gz.sha256`. The archive still extracts to `obey-voice-PLATFORM/`, containing
the executable, sibling `lib/`, notices and `manifest.json`. Keep that layout when
packaging or installing. The manifest and `--version` identify the same version.
The unversioned archive in `dist/` remains a local compatibility alias for Apple
app vendoring; release assets use versioned filenames.

The recipe verifies archive and per-file hashes, extracts elsewhere, runs the CLI
with loader overrides removed, and tests that removing each required library
causes a loader error. This is a packaging check, not a microphone or model test.
Run `just release artifact-test` for corruption, missing payload, version mismatch,
unsafe archive entries, incomplete publication and retry regression coverage.

## Publication

After the project's required checks pass, `just release tag vX.Y.Z` pushes an
annotated tag. **Release Artifacts** builds and validates both platforms before
its publication job starts. The [Homebrew lifecycle checks](homebrew-release.md)
also audit, install, bottle, upgrade and uninstall both platform packages in
isolated prefixes. It creates a draft release, uploads the complete set,
downloads and verifies it, publishes, then verifies published readback. A failed
build produces no public release; a failed upload/readback leaves a draft.
Prerelease tags are marked as prereleases. Publication never changes `Latest`;
package-channel promotion is separate and must reject downgrades.

Manual workflow runs on a branch build downloadable development artifacts without
publishing. A manual run on a release tag can retry publication. Published assets
are never replaced: a retry with identical bytes succeeds without mutations; a
different build for the same published version fails and requires a new version.
For an interrupted draft, rerunning can replace draft assets before verification.

For a local publication retry, download both platform archives and their sidecars
from the **same workflow run** into `dist/release`, then run:

```sh
python3 scripts/publish_release.py vX.Y.Z --check
just release publish vX.Y.Z
```

The check is offline. Publication requires an existing remote tag and a `gh`
session with repository release-write access. Never synthesize a missing platform
or publish a release containing only one archive. Do not use `gh release upload
--clobber` against a published release.

Homebrew tap publication, npm, Festival catalog publication and Developer ID signing/notarization
have separate qualification gates. An artifact workflow pass does not make those
channels available.
