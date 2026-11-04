package textclean

import (
	"errors"
	"strings"
	"testing"
)

const rawFunctionFixture = "<function=read_file>\n<parameter=path>\n/workspace/campaigns/example\n</parameter>\n</function>\n</tool_call>"

func TestToolMarkupFilterRejectsInvocationAtEveryChunkBoundary(t *testing.T) {
	for _, input := range []string{rawFunctionFixture, "<tool_call>" + rawFunctionFixture, "<parameter=path>", "</function>", "</tool_call>", "<function=read_file", "<tool_call", "</parameter"} {
		for split := 0; split <= len(input); split++ {
			var f ToolMarkupFilter
			got := f.Feed(input[:split]) + f.Feed(input[split:]) + f.Flush()
			if got != "" || !errors.Is(f.Err(), ErrRawToolMarkup) {
				t.Fatalf("input %q split %d: output %q err %v", input, split, got, f.Err())
			}
		}
	}
}

func TestToolMarkupFilterNeverLeaksByteStreamPayload(t *testing.T) {
	input := "I can check that. " + rawFunctionFixture + "Fabricated result."
	var f ToolMarkupFilter
	var out strings.Builder
	for _, b := range []byte(input) {
		out.WriteString(f.Feed(string(b)))
	}
	out.WriteString(f.Flush())
	if out.String() != "I can check that. " || !errors.Is(f.Err(), ErrRawToolMarkup) {
		t.Fatalf("output=%q err=%v", out.String(), f.Err())
	}
}

func TestToolMarkupFilterPreservesProseAndQuotedCode(t *testing.T) {
	for _, input := range []string{
		"Three < five and eight > six.", "x <function                    and y > z.", "x <functionValue and y > parameter.",
		"<em>normal markup</em>", "<tool_callback> is an unrelated identifier.",
		"The word function=read_file is not an invocation.",
		"Use `<function=read_file>` only as an example.",
		"```xml\n" + rawFunctionFixture + "\n```\nThis is quoted code.",
		"`` A literal ` followed by <function=read_file> in code ``",
		"A dangling <, <f, or <fun is ordinary partial text.",
		"<fun", "<function", "Use <generic`code` safely.",
	} {
		for split := 0; split <= len(input); split++ {
			var f ToolMarkupFilter
			got := f.Feed(input[:split]) + f.Feed(input[split:]) + f.Flush()
			if got != input || f.Err() != nil {
				t.Fatalf("input %q split %d: output %q err %v", input, split, got, f.Err())
			}
		}
	}
}

func TestToolMarkupFilterResumesDetectionAfterQuotedCode(t *testing.T) {
	var f ToolMarkupFilter
	input := "`<function=example>`\n" + rawFunctionFixture
	got := f.Feed(input) + f.Flush()
	if got != "`<function=example>`\n" || !errors.Is(f.Err(), ErrRawToolMarkup) {
		t.Fatalf("output=%q err=%v", got, f.Err())
	}
}
