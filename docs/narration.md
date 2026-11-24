# Narration and audiobooks

[Back to README](../README.md)

`obey-voice render` turns documents into audio files and a manifest without the
live voice pipeline (no microphone). It reads text, Markdown, HTML, URL articles,
or EPUB, segments the text, synthesizes with the configured TTS, and always
writes WAV (the source of truth).

```bash
# Single file (format auto-detected from the extension; --stdin reads text):
obey-voice render article.md --out out/article.wav
cat notes.txt | obey-voice render --stdin --out out/notes.wav
obey-voice render https://example.com/post --out out/post.wav   # URL article

# Sectioned multi-file: one WAV per heading/section + a manifest.
# Works for Markdown, HTML, URL, and EPUB (EPUB requires --out-dir):
obey-voice render article.md --out-dir out/article
obey-voice render book.epub --out-dir out/book

# Optional compressed output via an external encoder (default ffmpeg); WAV is
# still written. A missing encoder fails before any synthesis:
obey-voice render book.epub --out-dir out/book --audio-format mp3

# Resume a long render: unchanged chapters/sections are skipped, changed/failed
# ones rebuild. --json prints completed/skipped/failed counts and exits non-zero
# if any unit failed, so scripts can branch:
obey-voice render book.epub --out-dir out/book --resume --json | jq '.failed'

# Optional planning controls (defaults preserve prior behavior):
obey-voice render article.md --out-dir out/article \
  --max-segment-chars 1200 --pause-heading 750ms --pause-paragraph 400ms \
  --code-blocks skip
```

## Audiobook creation

`obey-voice audiobook create` is a task-oriented wrapper over the same render
runtime for EPUB books and digital PDFs: one WAV per chapter (EPUB spine) or
page (PDF) plus a manifest under `--out-dir` (required). It accepts render's
pass-through flags (`--resume`, `--voice`, `--speed`, `--audio-format`,
`--language`, `--encoder`, `--json`, `--manifest`, `--overwrite`). Qwen accepts
`--voice` and `--language`; its pinned CustomVoice model does not support
`--speed`. Use `obey-voice render` for
markdown, HTML, URL (including sectioned `--out-dir`), and text sources.

Before synthesis, build a reviewable production plan. This writes the
extracted sections, a YAML source of truth, and a Markdown preview; it does
not load TTS or create audio:

```bash
obey-voice audiobook plan book.epub --out-dir out/book
obey-voice audiobook review out/book/production-plan.yaml
# Apply explicit human decisions when needed:
obey-voice audiobook review out/book/production-plan.yaml --exclude contents --exclude body --reason "navigation/front matter"
```

The plan classifies likely navigation, index, front matter, main content,
reference, and back matter. Ambiguous sections remain `review` until a human
decides. Rendering from the approved plan will be added in a follow-up slice;
the current `create` command remains the direct raw-spine compatibility path.

```bash
obey-voice audiobook create book.epub --out-dir out/book
obey-voice audiobook create book.epub --out-dir out/book --audio-format m4b --resume --json
# From Calibre library (requires calibre_enabled=true and Calibre installed):
obey-voice config calibre_enabled true
obey-voice library list
obey-voice library search "cryptography"
obey-voice library show 42
obey-voice audiobook create --from-library "Crypto 101" --out-dir out/crypto
```

### Choose an audiobook narrator

Start with an EPUB or digital PDF in the language you want to hear. Obey Voice
reads the text in that edition. Choose the speech engine and then a voice from
that engine’s catalog.

| Engine | Narration languages | When to use it |
|--------|---------------------|----------------|
| Qwen3-TTS | Chinese, English, French, German, Italian, Japanese, Korean, Portuguese, Russian, Spanish | Choose a preset narrator and set the book language explicitly with `--language`. The language setting is independent of the speaker’s native accent. [Qwen3-TTS](https://github.com/QwenLM/Qwen3-TTS) |
| Kokoro | The built-in picker exposes 28 voices for **US and UK English**. Upstream Kokoro also has voices for **Mandarin Chinese, Japanese, Spanish, French, Hindi, Italian, and Brazilian Portuguese**. | Use built-in Kokoro for an English book, e.g. `af_heart` for US English or `bf_emma` for UK English. For a narrator in one of the other seven languages, connect a compatible Kokoro speech server through `openai-speech` and use the voice ID it serves. [Kokoro voice list](https://huggingface.co/hexgrad/Kokoro-82M/blob/main/VOICES.md) · [Speech server setup](openai-speech.md) |
| Phonon Multilingual | English, French, German, Spanish, Portuguese | Enable the multilingual pack and choose among 102 named voices, including regional choices such as Mexican and European Spanish. Its provider ID is `openai-speech`. [Phonon model card](https://huggingface.co/gradium/phonon-40m-multilingual) · [Pack setup](openai-speech.md#choose-the-full-phonon-catalog) |
| Audio8 Preview 0.1B | Primarily Chinese and English. The publisher reports experimental evaluations for German, Spanish, French, Italian, Japanese, and Korean. | Use the installed Audio8 export with its packaged or registered reference voice. Audition a passage to check its pronunciation. [Audio8 release guide](https://github.com/Edge0-AI/Audio8_TTS/blob/master/guides/models/preview-0.1b.md) · [Reference voice setup](audio8-voices.md) |

List narrators on your installation with `obey-voice voices --provider kokoro`
(or substitute `qwen3-tts`, `openai-speech`, or `audio8`). The listing
command inspects that provider. Rendering uses the active persona’s speech
settings, which can override the global provider.

**Example: a Japanese EPUB with Qwen3-TTS.** Create a dedicated persona once,
then select it for this job in a subshell. Replace `book.epub` with your book
and the sample sentence with a passage from it.

```bash
obey-voice persona create "Audiobook narrator" --no-activate
obey-voice persona edit audiobook-narrator --tts-provider qwen3-tts --voice Ono_Anna

(
  export ACTIVE_PERSONA=audiobook-narrator
  obey-voice models ensure --tts &&
    printf '%s\n' 'これは日本語の朗読サンプルです。' |
      obey-voice render --stdin --out sample.wav --voice Ono_Anna --language Japanese
)
```

Listen to `sample.wav` to check the names and pacing. To render the book
with the same settings, run the following command. On a second run, the manifest lets the renderer skip
completed chapters and pick up where it left off.

```bash
ACTIVE_PERSONA=audiobook-narrator obey-voice audiobook create book.epub \
  --out-dir out/book --voice Ono_Anna --language Japanese --resume
```

**For English with Kokoro**, use `--tts-provider kokoro --voice bf_emma`
in the persona edit command. Use `--voice bf_emma` on both render commands,
omit the Qwen-specific `--language` flag, and supply an English passage and book.

**Copy into your agent**

> Narrate [path to EPUB or digital PDF] in [language], with [accent or voice
> preference]. Choose an engine from the table above and list its available
> voices. Set up a dedicated audiobook persona with the chosen engine and voice,
> and select it for these commands using ACTIVE_PERSONA. Render a paragraph
> from the book to a WAV so I can listen to it. Once I approve, run audiobook
> create with the same settings and --resume. Save the chapters to [output
> directory] and report any failures.

## Calibre library (optional)

[Calibre](https://calibre-ebook.com) is free software that organizes ebooks on
your computer (EPUB, PDF, MOBI, …). You do **not** need it for voice chat.
Obey Voice can browse a Calibre library so you can pick books for audiobooks or
ask what titles you own.

**Typical setup (most users):**

1. Install Calibre and open it once so it creates your library:
   - macOS: `brew install --cask calibre`
   - Arch Linux: `sudo pacman -S calibre`
   - Debian/Ubuntu: `sudo apt install calibre`
   - Windows or other platforms: use the installer from calibre-ebook.com
2. Enable the integration: `obey-voice config calibre_enabled true`
   (or open **Library** in the TUI and press **e**).
3. Obey Voice finds `calibredb` on `PATH`, or in the macOS app bundle
   (`/Applications/calibre.app/Contents/MacOS/`), or `/opt/calibre` on Linux.
   Your default Calibre library is used when `calibre_library_path` is empty.

**If something is non-default:**

| Situation | Config |
|-----------|--------|
| Library not at Calibre’s default path | `calibre_library_path` |
| `calibredb` not found automatically | `calibredb_binary` (full path) |
| Prefer PDF over EPUB when both exist | `calibre_prefer_format pdf` |

`obey-voice doctor` reports `calibre-binary` as a **Warn** when missing (never a
hard failure). Voice and other features keep working.

In the TUI, open **Library** from the launcher to browse the catalog, search,
and view book details. From a book press **enter** or **a** to send an
EPUB/PDF path into **Create audiobook**; MOBI/AZW-family books are converted
to a cached EPUB with Calibre's `ebook-convert`. The audiobook screen's **Pick
from library** opens with a browsable catalog, and `/` switches to search.
Direct audiobook rendering still consumes EPUB/PDF after any library
conversion.

## Narrate pipeline (prompt-controlled)

```bash
obey-voice narrate plan article.md --out narration.plan.yaml
obey-voice narrate prepare narration.plan.yaml --resume
obey-voice narrate render narration.plan.yaml --resume
obey-voice narrate plan book.pdf --out out/book.plan.yaml   # requires pdftotext (Poppler)
```

Digital PDFs also work with direct render / audiobook create:

```bash
obey-voice render book.pdf --out-dir out/book
obey-voice audiobook create book.pdf --out-dir out/book
```
