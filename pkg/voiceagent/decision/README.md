# Optional decision experiments

This package provides a provider boundary, a conservative worker-route policy,
and a nonblocking pending result. The voice application does not construct it
or expose a setting yet. Its first consumer is `scripts/worker_decision_bench`.

`Evaluator` handles transport and structured questions. `SystemOne` supports
explicit local or Jev selection; it never chooses a provider from environment
variables or falls back to the cloud. Local transport requires loopback HTTP,
disables proxy forwarding and rejects redirects. Jev requires its official
HTTPS origin, a model and an explicitly supplied key. OpenAI needs a separate
adapter and acceptance policy; this package does not claim to support it.

`WorkPolicy` owns versioned questions and validates intent, tier confidence,
probability margin and enabled tiers. The questions and thresholds are an
experimental policy, not calibrated guarantees. Its output cannot specify a
command, worker model, provider, endpoint, permission or spending authorization.
The existing host must still admit the proposed tier and construct the task.

`WorkPolicy{}` is off. `Start` returns nil and makes no request. With an
evaluator, `Start` runs a bounded decision alongside work the host already has
to do. At the existing routing boundary, call `Take` once:

```go
pending := policy.Start(ctx, input, budget)
defer pending.Close()
// Existing admission/preparation happens here, without waiting for pending.
result, ready := pending.Take()
// Use only a ready, valid proposal for the same still-authorized turn.
// Otherwise continue today's route immediately.
```

`Take` never waits. It cancels unfinished inference and cannot consume a late
result afterward. Completed results survive their inference deadline while
waiting for admission, but parent cancellation and `Close` fence them out.
Evaluators must honor context cancellation. The provider does not retry.

The host owns additional integration guards: deterministic commands first;
skip background notifications and agent continuations; reject a changed input,
persona/conversation or worker-policy revision; preserve canonical history;
and cancel/fence outstanding proposals when disabled. Starting inference in
parallel can still contend for GPU resources. Nonblocking consumption alone
does not prove unchanged voice latency.

No process manager, installer, model download, live shadow hook or active route
is included. These belong at the owning application's existing configuration,
asset and admission boundaries after the selected policy meets its evaluation
gates. See the linked WI-fb8f26 work record for retained local measurements.
