// Package transcription turns a finite audio file into timed utterances.
//
// It is not the conversational path. Nothing here starts a turn, touches
// history, speaks, routes a tool or files a meeting: a run reads audio, finds
// speech spans, decodes each one and reports it with the sample positions it
// came from.
//
// Timing is absolute on the source timeline. The detector it drives is built
// for one file and is never cleared while that file is in flight, because
// clearing it resets the sample origin every span start is measured from.
package transcription
