# Voice stack

With local speech providers and Ollama, the conversation path runs on your hardware. Background workers can use a different model or provider. A remote brain or worker sends its requests to that provider.

```mermaid
flowchart TD
    mic[Microphone] --> vad[Voice detection]
    vad --> stt[Speech recognition]
    stt --> brain[Conversation and routing]
    brain --> chunks[Sentence chunks]
    chunks --> tts[Speech synthesis]
    tts --> speaker[Speaker]
    brain -. Task .-> worker[Optional background worker]
    worker -. Result .-> brain
```

Speech starts as sentences arrive. The latency target is under 2 seconds from the end of your utterance to first audio; actual timing depends on your hardware, model, and provider.

| Layer | Default | Options |
|-------|---------|---------|
| Brain | Local models via Ollama | Claude Code, [Codex](codex.md), Cursor, Grok, [Obey daemon](obey-daemon.md) |
| STT | NVIDIA Parakeet TDT (sherpa-onnx) | Whisper (`sherpa`), Zipformer streaming, whisper.cpp |
| TTS | Kokoro (sherpa-onnx) | native Qwen3-TTS, [OpenAI-compatible speech servers](openai-speech.md) |
| VAD | Silero | |
| Audio | miniaudio / malgo | |

Models download on first use into `models_dir`, and `just install` runs `models ensure` for the default Kokoro/STT/VAD stack. Qwen installs from TUI Settings or `obey-voice models ensure --tts` on macOS Apple Silicon (Metal) and linux/amd64 (CPU). The default speech stack and native Qwen3-TTS need no Python runtime.
