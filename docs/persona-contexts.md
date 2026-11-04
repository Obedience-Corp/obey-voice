# Shared conversations and persona context

The transcript is shared, with each assistant reply attributed to the persona
that produced it. Selecting another persona changes the responder's prompt,
model, voice, directory binding, and worker configuration. It does not make that
persona the author of earlier replies from someone else.

Each persona retains its own working conversation, compacted summary, memory
archive, and context-window expansion. On a switch, the outgoing context is saved
and the selected persona's context is restored. New shared turns are added with
their original attribution. A persona that has not participated starts from the
shared transcript, without another persona's private summary or memory archive.
These contexts survive saving and reopening the conversation. Clearing the
conversation clears its private contexts too.

Sessions containing persona contexts use format 4. This version still reads older
sessions, which initialize private contexts from their existing shared transcript.
Older binaries refuse format-4 sessions rather than silently dropping the contexts.

## Incomplete and runaway responses

Foreground Ollama replies default to a 4,096-token response allowance, shared
across tool rounds and automatic continuations. Set `ollama_response_tokens` to
adjust it, for example:

```sh
obey-voice config set ollama_response_tokens 8192
```

The allowance includes reasoning. A malformed tool-generation repair reserves and
charges its request allowance even when the server cannot report failed usage.
An additional streaming byte bound stops servers that ignore the output setting.
Reaching the limit reports an error and does not automatically start another
continuation. Worker and internal summary requests retain their separate policies.

An interrupted, failed, or output-limited partial answer remains intact in the
transcript with its disposition. Future model requests receive a marked, bounded
prefix of that incomplete answer so its tail cannot overwhelm the next response.
Previously saved turns without disposition metadata are not retroactively altered.

Raw tool invocation markup in a reply is rejected with a visible error. It is not
interpreted as permission to execute a tool or passed to speech. Markdown code
examples and ordinary comparisons remain valid text.

See [persona worker overrides](persona-workers.md) for worker inheritance and
configuration. Installation policy remains global; jobs retain the persona and
configuration captured when they were admitted.
