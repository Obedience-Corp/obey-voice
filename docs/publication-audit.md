# Audit a publication candidate

Use this procedure before changing repository visibility or publishing a new
source snapshot. Keep raw audit reports outside the repository: even a redacted
secret report can contain private filenames and surrounding text. Publication
also exposes repository metadata, discussions and retained workflow outputs.

## Select the publication boundary

Record the candidate commit, destination repository, included refs, release files
and hashes. Choose one strategy explicitly:

| Strategy | Review scope and tradeoff |
| --- | --- |
| Publish the existing repository | Preserves history and collaboration context; requires review of old/deleted content, tags, PR refs/discussions, issues and retained Actions outputs as well as HEAD. |
| Publish a reviewed snapshot in a new repository | Reduces the initial public history surface; preserve license, notices and attribution, establish the maintenance destination, and keep the original private. Inspect the snapshot itself fully. |

A new root commit pushed to the existing repository does not remove PR refs,
discussions, old artifacts or cached copies. Do not describe an orphan branch or
force-push as complete erasure. Any history rewrite or remote deletion requires
an explicit, concrete decision; the audit commands below only read remote state.

## Capture origin independently

Use an authenticated SSH remote and a private temporary directory. A fresh mirror
separates the actual advertised origin refs from local worktrees or other remotes.
It is a point-in-time snapshot; compare refs again before publication.

```sh
umask 077
audit_dir="$(mktemp -d "${TMPDIR:-/tmp}/obey-public-audit.XXXXXX")"
git clone --mirror git@github.com:Obedience-Corp/obey-voice.git "$audit_dir/origin.git"
git -C "$audit_dir/origin.git" for-each-ref \
  --format='%(refname) %(objectname)' > "$audit_dir/refs.txt"
git -C "$audit_dir/origin.git" rev-parse refs/heads/main > "$audit_dir/candidate.txt"
git -C "$audit_dir/origin.git" rev-list --all --count > "$audit_dir/commit-count.txt"
gh api --paginate repos/Obedience-Corp/obey-voice/releases > "$audit_dir/releases.json"
gh api --paginate 'repos/Obedience-Corp/obey-voice/actions/artifacts?per_page=100' \
  > "$audit_dir/artifacts.json"
```

Check the captured refs for `refs/pull/*` as well as heads and tags. A mirror
captures advertised refs, not every GitHub-side retained object. Inventory issues,
PR discussion and Actions logs separately using GitHub; do not assume they are
covered by a Git scan. Multi-page API output consists of consecutive JSON values.

## Scan history and the candidate tree

Gitleaks 8.30.1 was used to verify these commands. Record the installed version and
scanner configuration. Inspect any repository-level Gitleaks config/ignore files
and environment overrides before relying on results; do not silently allowlist
findings. Review the tool's completion logs for errors and skipped coverage.

```sh
gitleaks version > "$audit_dir/gitleaks-version.txt"
gitleaks git --log-opts='--all' --redact=100 --ignore-gitleaks-allow \
  --report-format=json --report-path="$audit_dir/history.json" \
  "$audit_dir/origin.git" > "$audit_dir/history.log" 2>&1

mkdir "$audit_dir/tree"
git -C "$audit_dir/origin.git" archive refs/heads/main > "$audit_dir/tree.tar"
tar -xf "$audit_dir/tree.tar" -C "$audit_dir/tree"
gitleaks dir --redact=100 --ignore-gitleaks-allow \
  --report-format=json --report-path="$audit_dir/tree.json" \
  "$audit_dir/tree" > "$audit_dir/tree.log" 2>&1
```

Run and inspect each scan separately: default exit status 1 means findings were
detected, not that the audit succeeded. Operational errors also need resolution.
Triage by rule, path, commit and line in private evidence. For a false positive,
record the source behavior that proves it is a non-credential (for example, a
deterministic content hash). Do not add a blanket exclusion to make scans green.
Confirmed credentials require rotation and scope review, even if absent at HEAD.

These pattern scans are not a complete confidentiality audit. Review historical
paths and content, commit messages and media manually. A screenshot, recording,
transcript or proprietary document can disclose information without resembling a
credential. A diff-based history scan also does not fully inspect binary blobs.

## Artifacts and final evidence

For each retained release or Actions artifact, record its ID, originating commit,
digest and disposition. Download into the private audit directory, inspect the
archive layout before extraction, and review packaged files, embedded assets,
licenses and metadata. Never execute an old downloaded binary to inspect it.
Use secret scanning as one check, not proof that binary/media content is cleared.
Expire/delete unreviewed retained outputs only after an explicit disposition
decision; an empty Releases page does not mean there are no retained artifacts.

Finish with a gate record containing:

- Exact reviewed refs, candidate commit and artifact hashes; rerun after changes.
- Scanner version/configuration, coverage, exit statuses and privately triaged findings.
- Asset provenance/content review and historical disclosure dispositions.
- Repository settings and external outputs still requiring review.
- Explicit remaining limitations and the owner's publication decision.

Return to the [public release checklist](public-release-checklist.md). A clean
credential scan alone does not close the history, asset or overall release gates.
