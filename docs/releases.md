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

For a coordinated stable release, run from a clean checkout of the current,
CI-passing remote main:

```sh
just release plan vX.Y.Z     # read-only plan and configuration/access blockers
just release create vX.Y.Z   # create or resume the same release
```

The operator must complete the applicable physical voice/release acceptance
before invoking `create`: it authorizes publication and the tap merge. The command
checks software/package evidence; it cannot certify microphone quality or replace
the [physical procedure](first-conversation-acceptance.md). No extra interactive
confirmation is required by the command. It uses the installed `gh` login with
write access to the engine and owning tap; registry credentials stay in the
owning publication workflows, not the local release configuration.

The command:

1. Checks channel configuration/access, the tap's verification contract, current
   main CI and checkout identity. Runs Go tests, the project build and packaging
   checks before creating a new annotated tag. An existing remote tag is resumed
   at its original commit, even if main has subsequently advanced.
2. Waits for the native release workflow and verifies both published native
   archives/manifests/checksums through authenticated and anonymous readback.
3. Creates or reuses a `release/obey-voice-vX.Y.Z` branch and draft tap PR.
   Builds both production bottles from the exact tap commit and verifies their
   provenance, checksums, platform pair and lifecycle evidence.
4. Stages bottles without overwriting differing assets, updates the formula from
   their metadata, and tests those exact staged bytes on both supported hosts.
5. Publishes the bottles, verifies anonymous public URLs, then marks the tap PR
   ready and merges using its verified head SHA. Repository reviews/protections
   are honored; a required review stops the command with a resumable result.
6. Tests the merged tap through public bottle installs on both hosts. Runs and
   verifies each other enabled channel. Exits successfully only after every
   enabled channel has public readback evidence.

Rerun the same `create` command after an interrupted upload, failed job or required
review. It rechecks remote state rather than trusting a local checkpoint, reuses
complete staged/published assets, retries failed jobs once per invocation, and
never replaces published files or retargets a tag. It rejects channel downgrades.
An inconsistent draft or incomplete published release stops for investigation.
Each workflow wait is bounded (one hour by default). Ctrl+C stops local waiting;
already-started GitHub jobs may continue. The local summary is
`dist/releases/vX.Y.Z/status.json`; it records incomplete stages and errors.

The tap verification workflows must be merged before first use. `plan` reports
missing formula/cask contracts and, for notarized releases, desktop signing
secrets before any new tag is pushed.

### Desktop archive and Homebrew cask

The desktop channel uses `"signing": "ad-hoc"` in
`packaging/release-channels.json`. `just release create vX.Y.Z` builds and
publishes the desktop app without Apple credentials, alongside the CLI and
Homebrew formula. The desktop archive still requires a clean release tag,
valid signatures, source/version metadata, checksums and public install checks.
Ad-hoc signing does not establish an Apple-verified developer identity or
notarization. Users may need **System Settings → Privacy & Security → Open Anyway**
after attempting to launch the downloaded app. The cask prints that notice;
the pipeline does not remove quarantine or disable Gatekeeper.

The v0.3.1 release contains only CLI archives and the Homebrew formula. Use a
new version after this change merges; its existing tag still uses the older
notarized-only workflow and is not retargeted. The two Homebrew packages remain
independent: `obey-voice` is the CLI formula and `obey-voice-app` is the desktop
cask, both in `Obedience-Corp/homebrew-tap`.

For future notarized releases, change the channel's `signing` to `notarized`
and configure these **repository Actions secrets** on
`Obedience-Corp/obey-voice`, using GitHub settings or `gh secret set`:

| Secret | Value |
|---|---|
| `APPLE_CERTIFICATE_P12` | Base64-encoded Developer ID Application certificate and private key export |
| `APPLE_CERTIFICATE_PASSWORD` | Password protecting that P12 export |
| `APPLE_SIGNING_IDENTITY` | Exact `Developer ID Application: … (TEAMID)` identity |
| `APPLE_NOTARY_KEY_ID` | App Store Connect notarization API key ID |
| `APPLE_NOTARY_ISSUER_ID` | Issuer ID for that team API key |
| `APPLE_NOTARY_PRIVATE_KEY` | Contents of the API key's `.p8` file |

Notarized mode requires Developer ID Application, not Apple Development.
The archive workflow runs only on a release tag. In notarized mode it builds without exposing
signing credentials to dependency build scripts, imports the private key into
a temporary keychain after compilation, signs nested code with hardened runtime,
submits the app to Apple, staples the accepted ticket, and checks Gatekeeper.
It then archives the stapled app and verifies the extracted copy. The keychain
is removed on completion or failure. See [Apple's notarization requirements](https://developer.apple.com/documentation/security/notarizing-macos-software-before-distribution).

The app includes the companion built from the same checkout, its native speech
libraries, dependency notices, and signed source/version metadata. Missing
standalone Rust crate notices are retrieved from the crate's exact upstream
commit; when the upstream supplies no accessible notice, its complete published
crate source and license declaration are retained. No floating upstream branch
is used for notice retrieval.

The coordinator verifies the archive checksum, source/version receipt, and
configured signing mode before uploading desktop assets to the native release.
Notarized mode also requires successful Apple notarization evidence.
Retries reuse published bytes; they never overwrite a differently signed build.
Anonymous download readback precedes generating `Casks/obey-voice-app.rb` and
opening its tap PR. The tap checks signatures and provenance, upgrades from the
prior cask when one exists, reinstall, uninstall and fixture-data retention.
For ad-hoc releases it verifies quarantine remains and Gatekeeper rejects the app,
then records that user approval is required. It does not launch the quarantined
app or claim Gatekeeper acceptance; the bundled engine is smoke-tested during
archive creation. Notarized mode additionally checks the stapled ticket,
Gatekeeper acceptance and the installed engine's version. The coordinator
merges with an exact-head guard and then verifies installation from tap main.
Repository review requirements still apply. A first cask is created only after
the first valid public app archive exists; no placeholder URL or checksum is
installed in the tap.

For a local packaging check with no Apple credentials:

```sh
just release desktop-preview v0.0.0
```

This creates an ad-hoc signed **preview**, which cannot be promoted by the
release command, even when ad-hoc releases are enabled.
`just release desktop-artifact vX.Y.Z` uses the signing mode configured in the
clean release tag. For a local notarized build, also set `APPLE_SIGNING_IDENTITY`
and `APPLE_NOTARY_PROFILE` (an existing `notarytool` keychain profile).
Normal `just desktop install` remains the local development install and does
not require signing credentials, tags, or a release.

### Adding channels

`packaging/release-channels.json` is the explicit channel inventory. Homebrew is
enabled. npm is **not configured** until launcher/platform packages and a publisher
exist; disabled channels are printed and recorded, never reported as published.

To enable npm, implement `release-npm.yml` on main with `workflow_dispatch` input
`release_tag`, and list **all** launcher/platform package names in the config.
The workflow must consume the verified native release, run package installation
checks, preserve published versions, reject dist-tag downgrades and finish only
after publication. Use the project's chosen registry authentication in that
workflow. Set `enabled: true` only when that publisher exists. The coordinator
then requires successful execution at the release tag, checks every package's
public npm version and `gitHead`, downloads its tarball and verifies SHA-512 SRI.
Missing workflow/package configuration fails before a new tag is created.

New channel kinds need an adapter with equivalent publication/readback semantics;
unknown enabled channels fail closed. Festival delivery remains separate until
its adapter and acceptance evidence exist.

### Native-only operations

For a deliberate native-only release, after the project's required checks pass,
`just release tag vX.Y.Z` pushes an
annotated tag. **Release Artifacts** builds and validates both platforms before
its publication job starts. It creates a draft release, uploads the complete set,
downloads and verifies it, publishes, then verifies published readback. A failed
build produces no public release; a failed upload/readback leaves a draft.
Prerelease tags are marked as prereleases. Publication never changes `Latest`;
package-channel promotion is separate and must reject downgrades.
The [Homebrew lifecycle checks](homebrew-release.md) separately audit, install,
bottle, upgrade and uninstall both platform packages in isolated prefixes.
Both Homebrew jobs must pass before promoting the tap package; they do not block
the independent native archive release.

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
