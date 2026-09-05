# Meeting audio fixtures

## product-marketing-meeting-90s.wav

First **~90 seconds only** of a real multi-person product marketing meeting
(not the full ~43 minute video):

- **Source:** [Product Marketing Meeting (weekly) 2021-06-28](https://www.youtube.com/watch?v=lBVtvOpU80Q)
- **Format:** 16 kHz mono PCM WAV (`yt-dlp` + `ffmpeg`)
- **Not committed** (large binary)

### Shared cache (preferred)

The fixture lives in a **user-level cache** so every git worktree reuses one
file and is only downloaded once per machine:

```text
${XDG_CACHE_HOME:-$HOME/.cache}/festival-voice/fixtures/meetings/product-marketing-meeting-90s.wav
```

```bash
just fetch-meeting-fixture          # no-op if already cached
FORCE_FETCH=1 just fetch-meeting-fixture   # re-download
just speakerflow                    # uses the shared cache
```

Overrides:

| Env | Purpose |
|-----|---------|
| `OBEY_VOICE_FIXTURE_CACHE` | Directory for the shared WAV |
| `OBEY_VOICE_MEETING_FIXTURE` | Exact path to a WAV file |
| `FORCE_FETCH=1` | Re-download even if cached |

A copy under this directory (gitignored) is still accepted as a fallback.

## zoom-call.wav

First **~2 minutes** of a public Zoom-style tutorial call (multi-speaker English):

- **Source:** [Example Zoom Meeting for Students](https://www.youtube.com/watch?v=DvO10dFJaFE)
- **Format:** 16 kHz mono PCM WAV (`yt-dlp` + `ffmpeg`)
- **Roster:** `tests/speakerflow/zoom-call-roster.json` (ground truth for 95% harness)
- **Not committed** (large binary)

```bash
just fetch-zoom-fixture          # no-op if already cached
FORCE_FETCH=1 just fetch-zoom-fixture   # re-download
```

Same cache directory as the marketing clip. Override with `OBEY_VOICE_ZOOM_FIXTURE` for an exact path.
Zoom accuracy tests skip when the WAV is absent (CI stays green).
