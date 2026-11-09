# Native-only managed decision inference

Status: accepted for implementation; hardware qualification limits below.

## Decision

Managed local decisions use a Go protocol adapter and owner-bound Go supervisor
with a checksum-pinned llama.cpp b11669 worker. The worker listens on a Unix
socket in a private directory. Only the application's bounded `choice`/`noul`
contract is exposed on its pre-bound loopback listener. Generic completions,
model switching, media and file/URL loading are not exposed there.

Setup downloads a pinned platform archive and full-precision GGUF for the
selected catalog entry. It verifies hashes, extracts into a staging directory,
materializes confined library links, records file hashes and atomically publishes
under an installation lock. Startup verifies the runtime and selected model.
No interpreter, pip, uv, torch, source build or conversion enters product setup
or inference. Offline reference comparisons and conversion remain development
activities. Legacy assets are preserved; they do not satisfy native readiness.

All five existing IDs and the default remain unchanged. Mapika scoring uses the
pinned model's prompt layout, option token logits and temperature calibration.
The adapter reads pre-sampling log probabilities, normalizes across the option
labels and applies the reference confidence formula. Liquid uses the native
SystemOne endpoint with choice confidence adapted to the pinned reference's
winning probability. Policy gates are unchanged. The literal prompt delimiters
are model protocol syntax; user-editable decision instructions still come from
the existing prompt resolver.

## Qualification and consequences

Full-precision Mapika artifacts matched all 48 retained policy outcomes per
model. Quantized artifacts were rejected after observed routing differences.
Liquid matches all 48 against the float32 reference; one of 48 differs against
its BF16 reference near the unchanged 0.90 intent gate (0.90465 BF16 versus
0.89978 native, 0.89948 float32). This is a conservative no-work result. These
observations are corpus evidence, not a guarantee of numerical equivalence on
all inputs. Source/native revisions and file hashes are in `models.json`.

Offline tensor lineage checks find identical Liquid tensors. Mapika tensors
match except the converted state-space `ssm_a` coefficients, whose maximum
relative difference is 1.09e-7 (float32 transformation rounding).

Installed setup/inference/shutdown is tested for all five models on Apple
Silicon and 0.8B CPU on Linux ARM64 in a Python-free Debian 13 container.
Linux x86_64 and CUDA have pinned artifact definitions and device-selection
coverage, but real NVIDIA inference remains unqualified. Linux upstream binaries
require glibc 2.38, recent libstdc++, OpenSSL 3 and libgomp; Debian 12 cannot load
them. This raises the managed runtime baseline; external local servers remain
available. CUDA libraries retain NVIDIA's toolkit terms. No measured memory
savings claim is made; UI memory figures are planning estimates.

The retained research evidence is tracked in campaign WI-17b4ff and implementation
WI-8acf53. Unit tests cover typed API restrictions, body limits, cancellation,
logit fallback, calibration, archive corruption/link safety, legacy assets and
native owner-death cleanup. `TestCatalogLive` is an opt-in installed-runtime
smoke test using an explicitly supplied disposable model directory.
