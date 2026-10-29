# Speech worker on another computer

Most people never need this page.

Obey Voice speaks on the computer you talk to. `speech-serve` exists for one
situation: **the GPU is a different computer than the one you talk to** — a Mac
on the couch, an NVIDIA box in the closet.

| Your setup | What to do |
|------------|------------|
| One computer | Nothing here. `tts_place` stays `local` and the worker runs in-process. |
| GPU in this computer (Metal or CUDA) | Nothing here. `tts_provider: qwen3-tts`, `tts_place: local`. |
| GPU in another computer | This page. |
| Laptop away from home, CPU only | Kokoro. Point `tts_worker_url` at a tailnet address if you want your home GPU. |

Obey Voice never starts `speech-serve` for you, and a normal install never binds
port 7263.

## On the speech computer

Install the native Qwen package, then run the worker service:

```bash
obey-voice models ensure --tts
obey-voice speech-serve
```

The first run prints a token, once. Copy it — it is written to
`~/.obey/agents/voice/obey-voice/speech/token` (mode 0600) and never printed
again. `OBEY_SPEECH_TOKEN` or `--token-file` override the file.

The default bind is `127.0.0.1:7263`, which reaches nothing but that computer —
useful for a smoke test, useless for two machines. To actually share the GPU,
bind an address the other computer can reach **and** give it TLS:

```bash
obey-voice speech-serve --bind 0.0.0.0:7263 \
  --tls-cert /path/cert.pem --tls-key /path/key.pem
```

On a tailnet, let Tailscale do both:

```bash
obey-voice speech-serve --tailscale
```

`speech-serve` refuses to start on a non-loopback address without TLS, and
refuses to run without a token at all. There is no "trusted LAN" mode: the
thing on the other end of that socket can make this computer's GPU work.

The model loads before the port opens (about six seconds on the lab worker), so
a port that answers is a worker that can speak. The worker then stays warm for
the daemon's life — conversations connect and disconnect without ever paying
for a reload.

### Flags

| Flag | Default | What it does |
|------|---------|--------------|
| `--bind` | `127.0.0.1:7263` | Address to listen on. Anything but loopback requires TLS. |
| `--token-file` | — | Read the shared token from this file instead of the credentials directory. |
| `--tls-cert` / `--tls-key` | — | Certificate and key PEM, same names as `obey-voice serve`. |
| `--tailscale` | off | Bind this node's tailnet address and mint a certificate with `tailscale cert`. |
| `--log-file` | — | Append output to this file (created 0600) instead of the console. |

## On the computer you talk to

```bash
obey-voice config set tts_provider qwen3-tts
obey-voice config set tts_place lan
obey-voice config set tts_worker_url https://speech-box:7263
obey-voice config set tts_worker_token <the token>
```

Settings → Qwen has the same switches — "Speech runs on", the address, the
token, and whether an unreachable worker should stop a conversation — if you
would rather not use the command line. The token is only ever typed there,
never displayed, and Settings refuses to switch computers (or to clear the
token) while that would leave the conversation with no way in.

`https://` off-box, `http://` only to `127.0.0.1`. The token never goes in the
URL. `OBEY_SPEECH_TOKEN` works here too — and one of the two is **required**:
`tts_place: lan` without a token is refused outright, so a missing token can
never look like an unreachable worker and quietly become Kokoro.

This computer needs no Qwen package, no GGUF and no GPU. It sends text and
plays audio.

Check it:

```bash
obey-voice doctor
```

The `speech-place` row dials the worker and reports host, round-trip and tier.

## When the speech computer is off

Talking still works. Obey Voice speaks with Kokoro on this computer and says so
once, at startup. That is the default because a speech worker that is not there
is a worse reason to be silent than a slightly different voice.

If you would rather not start at all than speak in the wrong voice:

```bash
obey-voice config set tts_lan_strict true
```

Then an unreachable worker is an error and the conversation does not begin. The
same setting turns the doctor's `speech-place` warning into a failure.

## What does not cross the network

- **Voice cloning.** A shipped clone voice — including `Veronica`, the default —
  is a reference recording on *this* computer, and the wire carries no audio
  files. Over LAN the speech worker speaks in **its own voice**. Obey Voice
  warns about this at the start of a conversation and in `doctor`; pick a
  preset voice (`qwen_tts_voice: Vivian`) for LAN speech, or set
  `tts_place: local` to clone.
- **Speech recognition.** Only synthesis is offloaded. Your microphone stays
  local.
- **Book rendering.** `obey-voice render` always synthesizes on this computer,
  so a render is reproducible from its manifest alone.
- **Phones.** Clients talk to `obey-voice serve` on port 7262
  ([serve protocol](serve-protocol.md)). Nothing but a conversation host ever
  dials 7263.

## One conversation at a time

`speech-serve` v1 serves a single client connection. A second connection is
refused immediately with an error frame rather than queued, so a forgotten
session can never silently hold the worker while a live one waits. Disconnect
the first host, or run a second worker.

If a client disappears mid-sentence, the worker is told to stop and the slot
stays held until that request has finished answering — a few seconds at most.
Frames are routed by request id as well, so audio meant for a client that left
is never delivered to the next one.

Ending a conversation does not stop the worker: the disconnect cancels whatever
was being spoken and leaves the model loaded. Only stopping `speech-serve`
itself (Ctrl-C, SIGTERM) shuts the worker down.

## Protocol

The wire is `obey-speech/v1`: newline-delimited JSON plus raw 24 kHz f32le PCM,
on one long-lived TCP connection. It is not HTTP, and it is not the serve
protocol. A client sends `hello` with the token, receives the worker's `ready`,
then exchanges `synthesize` / `pcm_meta` + PCM / `final`.

A Stage B worker answers `"stream": true` with several `pcm_meta` + PCM groups
instead of one, each carrying its 0-based `chunk` index, and a `final` totalling
`chunks` and `n_samples`. The bridge forwards all of it byte for byte — it never
had to learn those fields, because the chunk framing is the Stage A framing
repeated. The host counts the chunks and the samples against that final and
fails the request rather than playing short audio; `chunk` indices are what make
a lost frame visible at all, since the payload lengths still line up without it.

Whether chunks are requested is the host's decision, not the daemon's: the
`stream` field travels on each `synthesize` and `speech-serve` passes it
through unchanged. See `qwen_tts_stream` in
[configuration.md](configuration.md#chunk-playback-qwen-stage-b).
