# Homebrew packaging

The target is `obedience-corp/tap/obey-voice` in
[`Obedience-Corp/homebrew-tap`](https://github.com/Obedience-Corp/homebrew-tap).
The existing `lancekrogers/tap/samantha` formula remains independent.
The Obey Voice formula is not publicly available until the release and tap
publication steps below are complete.

Supported payloads are macOS 15+ on Apple Silicon and x86_64 Linux with glibc
2.39+ (Ubuntu 24.04 compatibility floor). Architecture requirements reject Intel
macOS and Linux ARM64. Linux installations below the glibc floor cannot pour a
bottle or fall back to installation from the native archive. No compiler, Node,
brain provider or model download is declared as a formula dependency.

The formula installs the complete native payload under `libexec/`, with a `bin/`
symlink to its executable. Its test checks the embedded version and manifest
inventory. Model setup and service startup are explicit user actions. It creates
no `samantha` alias and changes no user settings or model caches.
Homebrew can rewrite native library IDs and signatures during installation and
bottling. `upstream-manifest.json` preserves the original archive's identity and
checksums; it is not a checksum inventory of those relocated files. Homebrew's
bottle checksum protects the packaged installation.

## Generate and qualify

Obtain both platform archives and `.sha256` sidecars from the same validated
release run, placing them in `dist/release`. Use their exact version:

```sh
just release homebrew-test
just release homebrew-formula vX.Y.Z
just release homebrew-check vX.Y.Z dist/homebrew-check
```

Generation validates the archive hashes, per-file manifest and version before
writing `dist/homebrew/Formula/obey-voice.rb`. An incomplete or corrupted input
does not replace the output. The obsolete source-tarball `formula-sha` recipe is
replaced by this native-payload generator.

The lifecycle harness clones the installed Homebrew checkout into a new prefix,
uses separate HOME/config/cache/build directories, and creates a private test tap.
On macOS, choose a workspace outside the system temporary directory so Homebrew's
build sandbox can protect its checkout. The workspace must be empty. Existing
Homebrew installations and the user's app data are not modified.

The harness seeds Homebrew's download cache with verified artifacts while retaining
the canonical release URLs in the formula. This allows a development build to be
tested before release without fabricating a public URL or editing the binary.
It runs strict audit, real formula installation, `brew test`, bottle creation,
a formula-revision upgrade, uninstall, installation from the bottle, and final
uninstall. It verifies the bottle receipt, removal of package-owned files and
byte preservation of fixture settings/personas/sessions/model data. The revision
upgrade tests packaging lifecycle; it does not claim an application data migration
between different upstream versions.

Evidence and the command log remain in the workspace. `bottles/` contains the
test bottle, bottle JSON and canonical generated formula. Release Artifacts runs
this journey on native Linux and macOS and retains those outputs for seven days;
tap promotion requires both Homebrew journeys to pass. Native archive publication
remains independent. These are qualification
bottles built from a private fixture tap commit, not production tap artifacts.

## Publish the tap package

1. Publish and read back the versioned native release through the normal release
   workflow. Verify the actual release URLs before promoting a formula.
2. Render the formula from those published bytes. Open a PR in the owning tap
   adding `Formula/obey-voice.rb`; do not edit the Samantha formula or announce
   an install command before the tap entry is released.
3. From that tap's actual versioned checkout on both supported hosts, install
   with `brew install --build-bottle obedience-corp/tap/obey-voice`, run `brew test`,
   then build production bottles with:

   ```sh
   brew bottle --json --no-rebuild \
     --root-url https://github.com/Obedience-Corp/homebrew-tap/releases/download/obey-voice-vX.Y.Z \
     obedience-corp/tap/obey-voice
   ```

4. Stage those bottles in a draft release in the tap repository. Upload each
   `local_filename` under the corresponding `filename` from its bottle JSON;
   Homebrew's local and public filenames differ. Merge their JSON
   into the tap formula using `brew bottle --merge --write --no-commit` and both
   JSON paths. Preserve Homebrew's generated platform and relocation metadata;
   do not invent bottle checksums or claim a fixture-tap bottle as production.
5. Validate downloads and bottle installation on fresh supported hosts, including
   explicit Parakeet/Kokoro/provider setup and a real first voice conversation.
   Publish the bottle assets before merging the tap PR; verify the public install
   command, upgrade, uninstall and retained user data afterward.

Bottle assets belong to the tap's release, so adding them never mutates an already
published Obey Voice native release. Do not promote an older version over a newer
tap formula. Homebrew/core submission and the desktop cask are separate work.
