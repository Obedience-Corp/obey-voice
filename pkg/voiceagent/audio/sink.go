package audio

// Audible-sink kinds reported by SinkReporter. Pipeline type-asserts this
// interface on Engine; it must not import netapi to learn who is listening.
const (
	SinkNone         = "none"
	SinkLocalSpeaker = "local_speaker"
	SinkStreamClient = "stream_client"
)

// SinkReporter names the ear that will hear synthesized PCM.
//
// Fanout implements it. Test fakes that need a non-local sink implement it
// too. Started() is not this signal: it remains pcmReady.
type SinkReporter interface {
	SinkKind() string
}
