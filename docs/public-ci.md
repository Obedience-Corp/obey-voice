# Public repository CI boundary

Pull requests and `main` pushes run the same six CI jobs on disposable
GitHub-hosted Ubuntu 24.04 machines. The Linux release job uses that image too;
macOS releases use GitHub-hosted macOS. No workflow needs a self-hosted runner.
Job names and existing quality gates are unchanged.

CI grants only `contents: read`, does not persist checkout credentials, and does
not use repository secrets. The released-daemon integration receives the job's
read-only `GITHUB_TOKEN` to query public release metadata. Jobs have a 45-minute
limit. Release upload permission remains confined to the separate manual/tag
workflow; PR jobs do not download or promote artifacts from that workflow.

The full Go race suite has an explicit 20-minute per-package timeout. Its
echo-cancellation simulations exceeded Go's default 10-minute package budget
on hosted hardware while still making progress through the test cases. All
tests, signal-quality assertions and race instrumentation remain enabled;
the enclosing CI job still has a 45-minute limit.

## Required repository and organization settings

Changing `runs-on` is not a complete access boundary: a contributor can change
workflow YAML in a pull request and request a different runner. Before making
this repository public:

1. An organization administrator must verify that every organization/enterprise
   self-hosted runner group denies this repository access. Keep public-repository
   access disabled and remove any explicit grant to this repository. There must
   also be no repository-level self-hosted runner registration.
2. Keep the default workflow token read-only and disable Actions approval of pull
   requests. Verify fork PR policies do not send write tokens or secrets. Use the
   public-repository fork approval controls when they become available; approval
   is an additional review control, not a substitute for runner isolation.
3. Verify branch/tag rules limit who can change the default branch and create
   release tags. Only run manual releases from reviewed revisions. Never add
   `pull_request_target` or `workflow_run` execution of PR-controlled code to a
   privileged job.
4. After merging, run a harmless fork PR through the normal CI workflow and retain
   its job metadata. Confirm GitHub-hosted runner assignment, read-only token
   permissions and absence of repository secrets. Do not test isolation by
   executing malicious code on a trusted runner.

GitHub documents why public PRs and self-hosted runners require this separation:
[Adding self-hosted runners](https://docs.github.com/en/actions/how-tos/manage-runners/self-hosted-runners/add-runners)
and [Secure use reference](https://docs.github.com/en/actions/reference/security/secure-use).

## Verification and rollback

Run `actionlint .github/workflows/ci.yml .github/workflows/release-artifact.yml`
for workflow validation, then inspect all six checks on the PR. Linux artifact
CI exercises the same build and relocation smoke test as the Linux release job.
Manual/tag upload and macOS release execution are separate checks; a green PR
alone does not verify those paths or the organization settings above.

If hosted CI fails, diagnose the missing dependency or runner assumption on the
hosted runner. Do not restore public PR access to a trusted self-hosted machine
as a workaround. A rollback that needs self-hosted execution requires keeping
publication blocked until the isolation design is re-established.
