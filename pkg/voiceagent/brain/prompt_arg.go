package brain

import (
	"unicode/utf16"
	"unicode/utf8"
)

// argLimit is how an OS bounds a request passed to a CLI as one command-line
// argument: what the request costs, the most one more byte of it costs, and
// how much it may cost beside others, the CLI's other arguments that vary with
// the conversation.
type argLimit struct {
	unit    string
	perByte int
	cost    func(request string) int
	room    func(others ...string) int
}

// maxPromptArgBytes is the largest request passed to a CLI as one
// command-line argument on Linux and macOS. Linux refuses any single argument
// of MAX_ARG_STRLEN (131072 bytes, its terminating NUL included). Linux also
// bounds all the arguments and the environment together, to a quarter of the
// stack rlimit but never less than 128 KiB, so under a very small `ulimit -s`
// a request near this limit, the other arguments and a large environment can
// still exceed it. macOS has no per-argument cap; its ARG_MAX (1048576 bytes,
// getconf ARG_MAX) bounds the arguments and the environment together.
const maxPromptArgBytes = 100 << 10

// unixArgLimit measures a request in bytes, without its other arguments.
var unixArgLimit = argLimit{
	unit:    "bytes",
	perByte: 1,
	cost:    func(request string) int { return len(request) },
	room:    func(...string) int { return maxPromptArgBytes },
}

// windowsArgLimit measures the request and the other arguments together in
// UTF-16 code units, as CreateProcess bounds the whole command line. One more
// byte of request costs at most two units: a byte is at most one unit, and
// escaping at most doubles it (a quote, or a backslash before one).
var windowsArgLimit = argLimit{
	unit:    "UTF-16 code units",
	perByte: 2,
	cost:    windowsArgUnits,
	room:    windowsArgRoom,
}

// windowsCommandLineUnits is how much of a Windows command line, in UTF-16
// code units, the request and the CLI's other arguments that vary with the
// conversation may take together. CreateProcess refuses a command line of
// more than 32,767 units with its terminating NUL; the 2,767 left over are for
// the program path (MAX_PATH is 260), the flags, the model and session ids and
// the spaces between them. A CLI installed as a .cmd or .bat file runs under
// cmd.exe, which takes at most 8,191; that is not allowed for.
const windowsCommandLineUnits = 30000

// windowsArgUnits is how many UTF-16 code units s takes on a Windows command
// line, with the space before it, once quoted and escaped as Go passes an
// argument to CreateProcess (syscall.EscapeArg): a rune outside the Basic
// Multilingual Plane takes two, an invalid byte one, and a quote, or a
// backslash before one, is escaped with a backslash.
func windowsArgUnits(s string) int {
	units := 1
	if s == "" {
		return units + 2
	}
	quote := false
	for i := 0; i < len(s); i++ {
		if s[i] == ' ' || s[i] == '\t' {
			quote = true
		}
	}
	if quote {
		units += 2
	}
	slashes := 0
	for len(s) > 0 {
		r, size := utf8.DecodeRuneInString(s)
		s = s[size:]
		switch r {
		case '\\':
			slashes++
		case '"':
			units += slashes + 1
			slashes = 0
		default:
			slashes = 0
		}
		if n := utf16.RuneLen(r); n > 0 {
			units += n
		} else {
			units++
		}
	}
	if quote {
		units += slashes
	}
	return units
}

// windowsArgRoom is how many UTF-16 code units a request may take beside
// others.
func windowsArgRoom(others ...string) int {
	room := windowsCommandLineUnits
	for _, o := range others {
		room -= windowsArgUnits(o)
	}
	return room
}
