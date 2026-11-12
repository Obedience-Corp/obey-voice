# Persona worker overrides

Installation Settings supply worker defaults and global policy. In tiered mode,
add `brain.worker_tiers` to a persona's `persona.yaml` to customize its background
workers. This does not change other personas or jobs already admitted.

```yaml
brain:
  provider: ollama
  model: my-conversation-model
  worker_tiers:
    simple:
      enabled: true
      assignment:
        provider: ollama
        model: my-worker-model
        endpoint_ref: http://127.0.0.1:11434
        account_ref: null
        billing: local
      limits:
        runtime_seconds: 180
        model_requests: 12
        tool_calls: 30
        transport_retries: 1
        output_tokens: 4096
        charge_micro_usd: null
    complex:
      enabled: false
```

A tier's assignment applies only to work the persona hands to a worker. A
persona whose `brain.work` is `own` does its background work on its own
backend and model; the tier its spawn is routed to, with these overrides
applied, still sets that job's limits (see
[Who does the work](delegation.md#who-does-the-work)). Setting
`worker_tiers` does not by itself make `worker` the persona's default; naming
a `brain.worker` does.

Omitted tiers and fields inherit Settings. `enabled: false` explicitly disables a
tier. Assignment and limits replace their entire groups: supply a complete group
when overriding it. Remove `worker_tiers` or set it to null to restore inheritance.
Overrides are used only when installation Settings enable tiered mode.

Paid enablement, charge policy, daily allowance, concurrency, queue capacity, and
provider capability checks remain installation-wide. A persona cannot override
these controls. Paid assignments still require the existing explicit account,
billing, and uncapped-adapter consent rules.

The Persona editor preserves these overrides during model and voice edits and
reports whether tiered workers inherit Settings or have persona overrides. Edit
the YAML to configure tier overrides. Its existing Worker rows configure the
legacy singular worker and are inactive in tiered mode; existing legacy profiles
retain their behavior.

New jobs freeze the selected persona, working directory, resolved worker model,
limits, and configuration at admission. Selecting another persona or changing
Settings does not reassign queued or running work. Installation paid-disable
continues to apply to later paid dispatch requests.
