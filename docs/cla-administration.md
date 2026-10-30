# CLA administration

Obey Voice uses the hosted [CLA Assistant](https://cla-assistant.io/) for one-time
contributor acceptance and automatic PR status checks. The agreement grants
Obedience Corp additional proprietary-use rights; the public project remains
AGPL-3.0-only.

## Service configuration

- Repository: `Obedience-Corp/obey-voice` (link this repository only).
- Agreement: [Obedience Corp CLA v1.0](https://gist.github.com/lancekrogers/ec9ccefecdf2f4a7362b00681fe13518).
- Repository copy: [CLA.md](../CLA.md). Keep it identical to the Gist file.
- Require acceptance for all outside contributors; do not bypass small changes.
- Do not import a person as a signer unless they actually accepted this agreement.

A contributor accepts through the service using their GitHub account. The
service retains the agreement revision and acceptance record, and updates the
PR status automatically. Returning contributors do not sign again unless the
agreement changes. No routine maintainer ownership checklist is required.

## Activation and merge protection

Authorize the service, install its GitHub App for this repository, and link the
Gist in its dashboard. Verify that an unsigned contributor produces a failing
CLA status and a returning signer produces a passing status. Do not accept an
agreement on another person's behalf during testing.

Require the service's CLA status check in the `main` branch protection rule or
ruleset. GitHub currently refuses branch protection for this private repository
on its current plan; enable the required check when the repository becomes public
or its plan supports private-repository protection. Until then, a displayed status
alone does not prevent merging.

Export the acceptance records from the CLA Assistant dashboard for company
records. When changing the agreement, update the repository copy and Gist
together, increment the agreement version, and expect the service to request
acceptance of the revised terms. Existing grants are not retroactively changed.
