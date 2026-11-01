package audiojob

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func dubbingFixture(t *testing.T) (Config, Request, Provenance) {
	t.Helper()
	c, r, p, _ := textFixture(t)
	r.Task, r.Language, r.TargetLanguage = "translation", "en", "es"
	_, err := RunText(t.Context(), c, r, analyzeFunc(func(context.Context, []byte, TextTask) (string, TextUsage, error) {
		return "El borrador original.", TextUsage{PromptTokens: 400, CompletionTokens: 6}, nil
	}), p, func(Event) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join(r.OutputDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	findings, err := os.ReadFile(filepath.Join(r.OutputDir, "findings.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Removing inference cache must not destroy a reviewed translation.
	if err := os.RemoveAll(r.OutputDir); err != nil {
		t.Fatal(err)
	}
	r.Protocol, r.Task, r.Script = DubbingProtocol, "dubbing", "El texto revisado."
	r.TranslationReview = &TranslationReview{Reviewed: true, Manifest: string(manifest), Findings: string(findings)}
	r.JobID = "dub-1"
	r.OutputDir = filepath.Join(c.StorageRoot, r.JobID)
	return c, r, p
}

func TestDubbingSerializesReviewAndKeepsExplicitDuration(t *testing.T) {
	c, r, p := dubbingFixture(t)
	data, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var request Request
	if err := json.Unmarshal(data, &request); err != nil {
		t.Fatal(err)
	}
	if *request.TranslationReview != *r.TranslationReview {
		t.Fatal("JSON transport changed hash-bound evidence")
	}
	// Moving only the source path is permitted; bytes/range/revision stay bound.
	relocated := filepath.Join(c.StorageRoot, "moved.wav")
	if err := os.Rename(request.Input.Path, relocated); err != nil {
		t.Fatal(err)
	}
	request.Input.Path = relocated
	speech := &synth{}
	var events []Event
	manifest, err := Run(t.Context(), c, request, speech, p, func(e Event) error { events = append(events, e); return nil })
	if err != nil {
		t.Fatal(err)
	}
	if speech.calls != 1 || manifest.Protocol != DubbingProtocol || manifest.Transport != "reviewed_translation_then_speech" || manifest.Language != "en" || manifest.TargetLanguage != "es" || manifest.ApprovedScript != "El texto revisado." || manifest.ScriptSHA256 != hashBytes([]byte(request.Script)) || manifest.TranslationManifestSHA256 != hashBytes([]byte(r.TranslationReview.Manifest)) {
		t.Fatal("missing dubbing provenance", manifest)
	}
	// 61 seconds in and 0.01 seconds out stays a visible mismatch, never padded
	// or stretched in the worker. Timeline Apply must make its own decision.
	if manifest.Input.SampleCount != 61*16000 || manifest.Output.SampleCount != 240 {
		t.Fatal("duration silently changed", manifest)
	}
	saved, err := os.ReadFile(filepath.Join(request.OutputDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var restored Manifest
	if err := json.Unmarshal(saved, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.TranslationReview == nil || *restored.TranslationReview != *r.TranslationReview {
		t.Fatal("saved review did not survive")
	}
	for i, event := range events {
		if event.Protocol != DubbingProtocol || event.Seq != i {
			t.Fatal(event)
		}
	}
	if events[len(events)-1].Type != "completed" {
		t.Fatal(events)
	}
}

func TestDubbingRejectsStaleOrUnreviewedTranslationBeforeSpeech(t *testing.T) {
	cases := map[string]func(*Request){
		"missing":            func(r *Request) { r.TranslationReview = nil },
		"unreviewed":         func(r *Request) { r.TranslationReview.Reviewed = false },
		"corrupt-findings":   func(r *Request) { r.TranslationReview.Findings += " " },
		"invalid-manifest":   func(r *Request) { r.TranslationReview.Manifest = "{}" },
		"project-edited":     func(r *Request) { r.Source.ProjectRevision = "new" },
		"other-range":        func(r *Request) { r.Source.RangeID = "other" },
		"other-media":        func(r *Request) { r.Source.MediaSHA256 = hashBytes([]byte("other")) },
		"other-pcm":          func(r *Request) { r.Input.SHA256 = hashBytes([]byte("other")) },
		"other-origin":       func(r *Request) { r.Input.SourceOriginSamples++ },
		"languages-reversed": func(r *Request) { r.Language, r.TargetLanguage = "es", "en" },
		"no-script":          func(r *Request) { r.Script = " " },
		"invalid-utf8":       func(r *Request) { r.Script = string([]byte{255}) },
		"instruction":        func(r *Request) { r.Instruction = "Speak without review." },
		"wrong-protocol":     func(r *Request) { r.Protocol = Protocol },
		"wrong-task":         func(r *Request) { r.Task = "voice_conversion" },
		"missing-window": func(r *Request) {
			var findings map[string]any
			_ = json.Unmarshal([]byte(r.TranslationReview.Findings), &findings)
			findings["windows"] = []any{}
			raw, _ := json.Marshal(findings)
			r.TranslationReview.Findings = string(raw)
			var manifest Manifest
			_ = json.Unmarshal([]byte(r.TranslationReview.Manifest), &manifest)
			manifest.Output.SHA256, manifest.Output.Bytes = hashBytes(raw), int64(len(raw))
			raw, _ = json.Marshal(manifest)
			r.TranslationReview.Manifest = string(raw)
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			c, r, p := dubbingFixture(t)
			mutate(&r)
			speech := &synth{}
			_, err := Run(t.Context(), c, r, speech, p, func(Event) error { return nil })
			if err == nil || speech.calls != 0 {
				t.Fatal("invalid review reached speech", err)
			}
			if _, err := os.Stat(r.OutputDir); !os.IsNotExist(err) {
				t.Fatal("invalid review published an artifact")
			}
		})
	}
}

func TestDubbingUsesApprovedScriptAndTargetLanguageWithoutMutatingNarration(t *testing.T) {
	c, r, provenance := dubbingFixture(t)
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		if req.URL.Path != "/tts" {
			t.Error("dubbing triggered a different inference task")
			w.WriteHeader(400)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		lang, script := "es", r.Script
		if calls == 2 {
			lang, script = "en", "Approved words."
		}
		if body["lang"] != lang || body["input"] != script || body["voice"] != "female" {
			t.Error("changed approved speech", body)
			w.WriteHeader(400)
			return
		}
		w.Header().Set("Content-Type", "audio/pcm; rate=24000; encoding=float; bits=32")
		_ = binary.Write(w, binary.LittleEndian, make([]float32, 240))
	}))
	defer server.Close()
	runtime := &ManagedRuntime{endpoint: server.URL, voice: "female", client: server.Client()}
	speech, err := runtime.SpeechInLanguage(r.TargetLanguage)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Run(t.Context(), c, r, speech, provenance, func(Event) error { return nil }); err != nil {
		t.Fatal(err)
	}
	stream, err := runtime.Synthesize(t.Context(), "Approved words.")
	if err != nil {
		t.Fatal(err)
	}
	for range stream.Frames() {
	}
	if err := stream.Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.SpeechInLanguage("unknown"); err == nil {
		t.Fatal("unknown language accepted")
	}
	if calls != 2 {
		t.Fatal(calls)
	}
}

func TestDubbingFailureLeavesNoPublishedTake(t *testing.T) {
	for _, mode := range []string{"provider", "cancel", "output-limit", "source-changed"} {
		t.Run(mode, func(t *testing.T) {
			c, r, p := dubbingFixture(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			speech := &synth{fail: mode == "provider"}
			if mode == "output-limit" {
				r.Limits.OutputBytes = 44
			}
			var events []Event
			_, err := Run(ctx, c, r, speech, p, func(event Event) error {
				events = append(events, event)
				if event.Type == "progress" {
					if mode == "cancel" {
						cancel()
					}
					if mode == "source-changed" {
						file, err := os.OpenFile(r.Input.Path, os.O_WRONLY, 0)
						if err != nil {
							return err
						}
						_, err = file.WriteAt([]byte{1, 2}, 44)
						_ = file.Close()
						return err
					}
				}
				return nil
			})
			if err == nil {
				t.Fatal("failed job was accepted")
			}
			for _, event := range events {
				if event.Type == "artifact" || event.Type == "completed" {
					t.Fatal("partial take was published", event)
				}
			}
			if _, err := os.Stat(r.OutputDir); !os.IsNotExist(err) {
				t.Fatal("failed take remained")
			}
			stages, err := filepath.Glob(filepath.Join(c.StorageRoot, ".audio-job-*"))
			if err != nil || len(stages) != 0 {
				t.Fatal("incomplete stage remained", err, stages)
			}
		})
	}
}

func TestDubbingRejectsForgedHugeCoverageBeforePlanning(t *testing.T) {
	c, r, _ := dubbingFixture(t)
	var manifest Manifest
	if err := json.Unmarshal([]byte(r.TranslationReview.Manifest), &manifest); err != nil {
		t.Fatal(err)
	}
	r.Input.SampleCount = 1 << 50
	manifest.Input = r.Input
	raw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	r.TranslationReview.Manifest = string(raw)
	if err := r.Validate(c); err == nil {
		t.Fatal("enormous unmatched coverage accepted")
	}
}
