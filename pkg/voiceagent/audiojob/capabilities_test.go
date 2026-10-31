package audiojob

import "testing"

func TestAudioCapabilitiesKeepInstallationSupportAndQualitySeparate(t *testing.T) {
	for _, mode := range []string{"healthy", "no-audio-input"} {
		t.Run(mode, func(t *testing.T) {
			c, _ := managedFixture(t, mode)
			runtime, provenance, err := StartManaged(t.Context(), c)
			if err != nil {
				t.Fatal(err)
			}
			defer runtime.Close()
			doc := runtime.Capabilities(c.Runtime, provenance)
			if doc.ModelDigest != c.Managed.Model.SHA256 || doc.ProjectorDigest != c.Managed.Projector.SHA256 || doc.InputFormat != "wav_pcm_s16le" {
				t.Fatal(doc)
			}
			for _, task := range doc.Tasks {
				if !task.Installed || task.Qualified || task.Reason == "" {
					t.Fatal("installation incorrectly qualified a task", task)
				}
				switch task.Task {
				case "narration":
					if !task.Supported || task.Protocol != Protocol {
						t.Fatal(task)
					}
				case "transcription", "understanding":
					if task.Supported != (mode == "healthy") || task.Protocol != TextProtocol {
						t.Fatal(task)
					}
				default:
					if task.Supported {
						t.Fatal("unsupported task advertised", task)
					}
				}
			}
		})
	}
}
