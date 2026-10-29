package pipeline

import (
	"regexp"
	"strings"
)

// A Qwen worker appended this standalone handoff paragraph to an otherwise
// complete reply on 2026-10-28. Worker results are delivered directly; this
// instruction has no recipient there. Match only the observed trailing form,
// leaving quotations, inline discussion and the worker's raw thread intact.
var workerHandoffSuffix = regexp.MustCompile(`(?i)(?:\A|\r?\n[\t ]*\r?\n)[\t ]*Now give the result to the user as [^,\r\n]+,[\t ]*briefly[\t ]*\(2[-–]3 sentences\)\.[\t ]*No markdown\.[\t \r\n]*\z`)

func cleanWorkerReply(raw string) string {
	loc := workerHandoffSuffix.FindStringIndex(raw)
	if loc == nil {
		return raw
	}
	if answer := strings.TrimSpace(raw[:loc[0]]); answer != "" {
		return answer
	}
	return "The worker finished without an answer I can read aloud."
}
