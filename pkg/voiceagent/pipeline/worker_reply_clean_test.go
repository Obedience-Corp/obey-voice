package pipeline

import "testing"

const leakedWorkerHandoff = "Now give the result to the user as Obey, briefly (2-3 sentences). No markdown."

func TestWorkerHandoffSuffix(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"observed", "There are three active festivals.\n\n" + leakedWorkerHandoff, "There are three active festivals."},
		{"only instruction", leakedWorkerHandoff, "The worker finished without an answer I can read aloud."},
		{"other persona", "Done.\r\n\r\nNow give the result to the user as Santa Claus, briefly (2–3 sentences). No markdown.\r\n", "Done."},
		{"plain answer", "No markdown is needed.", "No markdown is needed."},
		{"quoted", "The model said:\n\n\"" + leakedWorkerHandoff + "\"", "The model said:\n\n\"" + leakedWorkerHandoff + "\""},
		{"inline explanation", "It appended: " + leakedWorkerHandoff, "It appended: " + leakedWorkerHandoff},
		{"followed by discussion", leakedWorkerHandoff + "\n\nThat instruction should not be spoken.", leakedWorkerHandoff + "\n\nThat instruction should not be spoken."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := cleanWorkerReply(tc.input); got != tc.want {
				t.Fatalf("reply = %q, want %q", got, tc.want)
			}
		})
	}
}
