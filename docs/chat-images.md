# Images and GIFs in chat

Paste an image or a local PNG, JPEG, or GIF path into the chat composer. Paths copied
from a terminal can be quoted or backslash-escaped, including screenshots with spaces
in their names. Imported images appear before you send and remain in the conversation
thread after sending. You can send images with a question or on their own.

## Terminal

- Paste with the normal clipboard shortcut, or use `/attach /path/to/image.png`.
- F2 pauses or resumes GIF previews. Reduced-motion mode shows a still frame.
- F3 opens the most recent draft image (or most recent thread image) at original size.
- F4 removes the last draft image. `/image N` opens thread image N, counting from one.

Inline previews use colored half-blocks, so scrolling and tmux work without a terminal
image protocol. Use the original viewer for detail. Native bitmap clipboard import
is supported on macOS and Linux; pasted file paths also work where a bitmap clipboard
reader is unavailable. On Windows, open the stored file in your desktop image viewer.

## Desktop

Paste a clipboard image or file path while the composer is focused, or drop image files
into the conversation. Each preview has **Remove** and **Open original** controls.
GIFs animate in the composer and thread; **Pause GIFs** displays a still frame.
Copy a GIF file path or drop the file to preserve animation: an operating system's
bitmap clipboard may contain only one frame.

## Model input and saved conversations

Image input works with vision-capable Ollama models and the Codex app-server
adapter, including background workers and group conversations. The actual recipient
is validated before inference. Claude, Cursor, Grok, Muse Spark, and Obey daemon
adapters currently reject image input explicitly. GIFs send their first frame as PNG;
their original animation remains available in the UI. Codex replay resends retained
images when it starts a new native thread, with a 32 MiB aggregate limit.

Agents can return local PNG, JPEG, or GIF files created through their existing tools.
They attach files using a standalone Markdown image reference:

```markdown
![Generated chart](</absolute/work/directory/chart.png>)
```

The file must be inside the agent's work directory (or the existing attachment store).
The host validates and copies it into durable storage, replaces the reference with its
caption, and displays the preview with the reply. Up to four references are processed;
missing, invalid, out-of-directory, and remote images produce visible diagnostics.
Fenced examples and ordinary filenames do not import files. Paths are kept out of
speech. Image generation remains the responsibility of the agent's available tools.

Requests to find or create an image must reach a worker that can obtain the file.
Saying "attached below" or naming a directory does not attach anything. The live
Ollama router retries explicit image-delivery claims once if they contain neither
a file reference nor a tool call. A repeated unsupported claim produces an error
instead of entering speech and chat history. This shares the required routing
step's single retry; an honest refusal selects `route(kind=chat)` before answering.
The check uses the current request to recognize pronoun-only claims such as
"I've attached them below" and also validates the answer after `route(kind=chat)`.
An unsupported post-chat claim produces an error without another tool loop.
Post-chat answers to image requests wait for completion before speech so rejected
claims cannot leak through streaming; ordinary chat and workers retain incremental
streaming. This is not
a general verifier of everything a model says. Explicit file references still
pass through the normal file validation above.

Returned images also appear in worker threads and desktop background-result cards.
A queued worker keeps the images from its originating request; cancelling foreground
speech or switching personas does not retarget its images to a different conversation.
Outbound assets remain presentation attachments; Ollama sends user images as vision
input, while returned files keep their caption in subsequent model history.

Imports are copied into the session attachment store. Saved history contains references,
so deleting the source screenshot does not remove it from a resumed conversation.
Missing or changed stored assets produce an explicit error. Attachment storage is local;
sending a message supplies its images to the configured Ollama server or Codex provider.

Limits: four images per message, 8 MiB per image, 4096 pixels per side, 16 megapixels.
GIFs allow up to 64 frames and 16 million total frame pixels. Preview caches are bounded;
older images can still be opened at full size. Desktop offers **Load preview** when its
cache is full. Removing an attachment from a draft does not delete the durable asset.

## Review recording and isolated verification

![Image and GIF paste, preview, and send](../demos/chat-images.gif)

Run `just demo-chat-images` to reproduce the VHS recording. It runs the real Bubble Tea
conversation UI with deterministic image fixtures and a fake turn runner, no microphone,
audio, personal configuration, or remote model. The tape is `demos/chat-images.tape`.

For native UI acceptance, run `just test images` from `apps/desktop`, then launch the
printed **Obey Voice Images Test.app**. The fixture provides a screenshot, animated GIF,
temporary session storage, and a local vision stub. It records model requests under the
printed temporary directory so attachment transport can be checked as well as appearance.

### Agent reply recording

![Image input and worker image/GIF reply](../demos/agent-images.gif)

Run `just demo-agent-images` for the actual Bubble Tea UI with deterministic worker
completion events. This recording proves display and interaction; separate local HTTP
and app-server subprocess tests verify native image payloads, worker admission,
returned-file import, session persistence, and group handoff.

`go test ./internal/tui -run TestImageClaimRepairReachesTerminal` exercises an
unsupported claim, its correction into a worker call, file import, and both PNG
and GIF previews through the real pipeline and terminal loop with a local model
stub. To check routing with an installed Ollama model and isolated history, run:

```sh
OBEY_VOICE_IMAGE_ROUTING_MODEL=lfm3:1.7b go test -tags=integration ./pkg/voiceagent/brain -run TestLiveImageRequestsRouteToWorker -count=1 -v
```

This opt-in check makes local model requests but does not execute worker tools.
Set `OBEY_VOICE_IMAGE_ROUTING_TIERED=1` too to require an explicit worker tier.

After `just bundle` in `apps/desktop`, run
`python3 scripts/ui-fixture.py --agent-images` for a silent native app whose local
model stub returns both a PNG and an animated GIF. The isolated request log and saved
session files let reviewers verify input bytes and durable reply references.
