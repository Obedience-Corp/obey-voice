# Phonon speech on macOS, Vulkan, and CUDA

Status: proposed; implementation and release acceptance are pending.
Work item: **WI-be5d71**, `explore-obey-voice-cross-platform-speech-2026-10-30`.
Campaign source: `gpd-camp/workflow/explore/obey-voice-cross-platform-speech/`.

## Problem

On an EndeavourOS machine with a Radeon 890M and Ollama 0.40.13, installing
Obey Voice succeeds but speech setup fails while pulling Phonon:

```text
this model requires MLX support, but the MLX runtime is not available
```

The compiled TTS default is Kokoro, but the default Obey persona selects
`openai-speech` and Harper. Persona application during `config.Load` makes
this a fresh-install problem as well as an existing-configuration problem.
Changing the global TTS default does not resolve that override.

The [Ollama Phonon package](https://ollama.com/library/phonon) currently
requires Apple Silicon. Generic Ollama Vulkan or CUDA support does not
establish that this model package can run. Gradium's separate
[native runtime](https://github.com/gradium-ai/xn-ptts) has Vulkan and CUDA
features and an OpenAI-compatible speech server.

## Direction

Retain the existing `openai-speech` client and builtin Phonon voice identities.
Preserve working Mac Ollama installations. Provide a managed native Phonon
server for Linux Vulkan and CUDA, and retain operator-managed external
endpoints. Kokoro remains a CPU alternative with its own voice namespace;
existing Qwen runtime support remains independent.

Requested persona/provider/voice, selected server/runtime/model, and observed
execution backend must be distinguishable. Determine capabilities at the
endpoint: a Linux client can talk to a Mac server, and a Mac client can use a
Linux worker. Client OS cannot decide whether an arbitrary endpoint can run
Phonon.

## Evidence collected on 2026-10-30

| Target | Observed result | Remaining limit |
|---|---|---|
| Radeon 890M, RADV, Mesa 26.2.6 | Native Phonon F32 server reported Vulkan on the 890M; Obey Voice rendered Harper, Juniper, Edwin and Zoey into nonzero 24 kHz WAVs. | Prototype build; no managed package or live conversation acceptance. |
| Linux transcription | Cached Parakeet decoded a generated 16 kHz utterance exactly. | CPU/file-based proof; microphone and brain loop untested. |
| M4 Max Mac Studio, Ollama 0.40.12 | Direct speech API synthesized all four named voices. | Installed renderer requested unavailable `VeronicaDesk` despite an explicit voice override; source revision/cause needs investigation. |
| archdtop | Historical Qwen CUDA evidence exists in `docs/benchmarks/F8-qwen-tts-cuda-2026-08-27.json`. | Current native Phonon CUDA execution is unverified; Tailscale SSH requires account/check completion. |

The prototype used native source revision
`a6bb4161f15afb29f3f7dd5a4d1b0d9e082c47e7`, reporting v0.5.0, and
`gradium/phonon-31m-en` snapshot
`8c64b55ec301728e2d1695bfea87a08a267d0c2d`. Its four voice embeddings were
selected from Gradium's catalog; production must pin the catalog too.
Short named-voice smoke requests measured first PCM at 33–44 ms on Vulkan
and 5–22 ms on Mac Ollama. These are feasibility samples, not controlled
benchmarks or audible-latency guarantees.

Offline cached native synthesis, unknown-voice rejection, model-ID mismatch
rejection, and disconnect recovery were exercised. An immediate request
after a disconnect returned 429 once; the subsequent retry completed.
Focused config/persona/Qwen tests and the OpenAI speech client suite passed
against the exploration baseline. Those checks do not test the proposed
implementation.

## Resolver and diagnostics

Use one resolution contract for setup, status, doctor, Settings, live speech
and batch render. Represent model presence, voice readiness and execution
capability separately. Capability states should include supported,
unsupported and unknown; a version or model listing alone cannot prove
successful synthesis.

Known managed-runtime metadata can inform readiness. For external endpoints,
keep uncertainty explicit and classify actual runtime rejection consistently.
Offer a bounded synthesis probe as an explicit validation action. Reading
status or browsing Settings must not download or load a model.

An MLX rejection must name the model/endpoint and offer a Linux native-server
setup path or an explicitly selected supported alternative. Do not repeat
`models ensure` as the sole remedy. Non-Ollama model mismatch errors should
show the server's advertised IDs and the configuration key to change, rather
than suggesting `ollama pull`.

## Provisioning and process lifecycle

Package pinned Linux amd64 Vulkan and CUDA HTTP-server artifacts with
checksums, provenance and declared native-library requirements. The tested
binary links libopus and libmp3lame as well as standard runtime libraries.
Upstream CPU CLI archives are not GPU server packages.

Use the existing managed-Qwen installer as a reference for layout,
verification and atomic installation, while keeping Phonon ownership separate.
Provision the model and actual named-voice embeddings; descriptive
`voices.jsonl` alone is insufficient. The tested native GPU path uses F32:
upstream quantized weights such as q8 run on CPU.

Supervise the owned server through bounded readiness, crashes, cancellation
and shutdown. Handle occupied ports, invalid checksums, interrupted extraction,
missing libraries/drivers and unavailable devices explicitly. Bind local
servers to loopback without replacing an external Ollama installation.
Keep existing authenticated LAN speech-worker routing distinct from direct
OpenAI endpoint configuration.

The runtime selector/configuration API and packaging ownership need a final
decision before implementation. An explicit external URL or model must not
be replaced by automatic local-runtime selection. Diagnostics must report
requested and actual execution separately.

## Persona and migration rules

Builtin Phonon personas should retain their voice names when a compatible
native server is selected. Preserve user-owned profiles, explicitly configured
endpoints/models, pinned tags and authentication. Use existing declaration
and persona-ownership metadata instead of rewriting saved profiles by host OS.
Synced persona identity must remain portable across machines.

Never silently send a Qwen clone name or Phonon voice to a different engine.
An unsupported explicit choice needs an actionable refusal or a visible,
explicitly enabled fallback policy. Trace the installed Mac renderer's
`VeronicaDesk` request before attributing it to current source. Preserve the
user's Mac configuration while reproducing it.

Parakeet STT, Silero VAD, speaker processing and audio-device handling stay
within the acceptance matrix. The inspected Parakeet, Kokoro and sherpa
speaker paths use CPU; adding GPU TTS does not accelerate the whole pipeline.

## Implementation map

| Area | Current source / proposed location | Required behavior and error paths |
|---|---|---|
| Target/readiness | `pkg/voiceagent/config/openaispeech_target.go:42` | Model, voices and capability are separate; external endpoints can be unknown. |
| Setup | `pkg/voiceagent/config/ensure_openaispeech.go:25` | Shared resolution, atomic assets, useful runtime rejection, cancellation preserved. |
| Status / doctor / Settings | `cmd/obey-voice/cmd/models.go:84`, `pkg/voiceagent/config/doctor_openaispeech.go:16`, `internal/tui/settings_speech_setup.go:42` | Consistent readiness and remedies; browsing remains read-only. |
| Native management | proposed `pkg/voiceagent/phonon/`; reference `pkg/voiceagent/qwen/native.go` | Verified packages, model/voice installation, owned process lifecycle and backend evidence. |
| HTTP client | `pkg/voiceagent/tts/openaispeech/openaispeech.go:114` | Correct model IDs, contextual errors, bounded streaming/disconnect behavior. |
| Persona application | `internal/persona/apply.go:17`, `internal/persona/persona.go:505` | Preserve explicit choices and ownership; resolve compatible runtime without losing voice identity. |
| Render | `cmd/obey-voice/cmd/render_real.go:248` | Actual request honors resolved provider-specific voice and explicit overrides. |
| Install summary | `justfile:356` | Distinguish binary installation from incomplete speech setup; decide strict-mode exit policy. |

Anchors refer to the exploration baseline `5a388c5b`; recheck against changes.
The branch starts at `2ddbec77` on main, whose intervening changes concern
image requests.

## Acceptance and delivery

1. Establish shared resolver/diagnostic contracts with fake endpoints covering
   MLX rejection, remote Mac from Linux, compatible native server, unknown
   capability, custom/pinned models, auth/listing failures and no side effects
   from discovery.
2. Package and validate native Vulkan/CUDA setup and lifecycle on actual
   hardware: all four voices, correct device, model ID, streaming, busy handling,
   offline use, crashes and failed/partial installations.
3. Verify fresh/upgrade/synced configuration, persona ownership, explicit
   overrides and actual HTTP voice requests across setup/live/render/UI.
   Existing Mac behavior must remain supported; investigate its observed
   renderer issue separately from server compatibility.
4. Complete microphone → STT → brain → TTS → audible playback on Mac, the
   handheld and archdtop. Record runtime/driver revisions, device evidence,
   latency, memory and concurrent-load effects. Update install, configuration
   and speech documentation with tested remedies.

The draft PR establishes this design and its implementation branch. It does
not yet claim managed runtime support or three-machine product acceptance.
