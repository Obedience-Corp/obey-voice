# Desktop monorepo acceptance — 2026-10-23

## Automated checks

- `just desktop check`: formatting, file size, Clippy, source resolution, Go vet,
  29 Rust tests, Go bridge race suite, and debug builds passed.
- `go test -race ./cmd/obey-voice/cmd ./internal/tui`: passed.
- `just build`: engine CLI build/vet passed.
- `just desktop bundle`: local macOS bundle built and strict signature verified.
- Source guard resolves the enclosing engine normally and with GOWORK=off;
  an overriding workspace pointing to a different engine checkout was rejected.
- Git ancestry confirms engine 826bf0ac and desktop 8673e2ca are both retained.

## Native application

Used `just desktop ui-fixture` with a temporary install and scripted local model.
No personal configuration, microphone, or live provider was used.

- Persona editor: light and dark themes inspected; centered, grouped fields,
  optional folder section and visible footer at the 900×620 minimum window.
- Engine schema provided Codex, audio8 and openai-speech options. Created
  Monorepo acceptance with Codex and openai-speech/default; reopened it and
  verified these values in its temporary persona.yaml.
- Speech setup: saved openai-speech, observed server/model/voice/key/timeout
  fields and confirmed a voice edit wrote openai_speech_voice in config.yaml.
- Typed/silent exchange returned the mock provider response. Saved and resumed
  it in the UI; the session JSON contains both the user and assistant turns.
- `just desktop test work`: replay showed 1 active and 1 queued while new tasks
  were disabled. Selecting the cue opened Workers; returning kept the cue.
  Completing both jobs cleared it without replacing the conversation status.

The native automation connection required a window resize to resume dispatching
some queued accessibility actions; no application data was lost. The checks
above were verified after reconnecting/resizing rather than inferred from input.

The saved-voice preview regression also checks that openai-speech uses its own
voice value, never Kokoro’s or a nonexistent generic voice key. This carries the
relevant correction identified in the concurrent original-app PR #13; live audio
preview was not exercised.

## Limits

Linux CI is added but local native inspection was macOS only. Hardware voice,
real speech servers, CLI account authentication, and distribution notarization
were not exercised. The bundle is locally ad-hoc signed, not installed/published.
