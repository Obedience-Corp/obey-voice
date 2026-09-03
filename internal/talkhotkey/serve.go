//go:build windows || (darwin && cgo)

package talkhotkey

import "bufio"

// What both native helpers share: one registration id, and the one place a
// key edge becomes a line on stdout.

// hotkeyID is our one registration. Both platforms deliver events through a
// handler that also sees ids we did not register, so the id is checked rather
// than assumed.
const hotkeyID = 1

type lineWriter struct{ out *bufio.Writer }

func (w *lineWriter) write(event Event) {
	line, err := event.Marshal()
	if err != nil {
		return
	}
	_, _ = w.out.Write(append(line, '\n'))
	_ = w.out.Flush()
}
