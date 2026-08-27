package config

// SortformerDiarizerName is the transcribe.cpp model slug behind
// speaker.meeting.diarizer=sortformer. There is deliberately no key to pick
// between weights the way transcribecpp_model does: v1 ships exactly the model
// the T3 gate measured.
const SortformerDiarizerName = "diar_streaming_sortformer_4spk-v2.1"

// SortformerMaxSpeakers is the model's hard ceiling (the 4spk in its name). It
// has no speaker-count input, so this is a limit, not a target.
const SortformerMaxSpeakers = 4

// SortformerModelRevision stamps Observation.ModelRev on every span the
// diarizer produces, so a stored timeline records which model wrote it.
const SortformerModelRevision = SortformerDiarizerName + "-Q8_0"

// SortformerDiarizerAsset is the managed Q8_0 GGUF. Published quants are
// F32/F16/Q8_0 only (no K-quants), and Q8_0 is the one T3 measured. The
// weights are under the NVIDIA Open Model License rather than the runtime's
// MIT, so the license travels with the asset the way the STT GGUF's does.
func SortformerDiarizerAsset() TranscribeCPPAsset {
	return TranscribeCPPAsset{
		Name:      SortformerDiarizerName,
		Languages: "language-independent (speaker segmentation only, no transcription)",
		Filename:  SortformerModelRevision + ".gguf",
		URL:       transcribeCPPGGUFBase + SortformerDiarizerName + "-gguf/resolve/main/" + SortformerModelRevision + ".gguf",
		SHA256:    "a5dacdc650790266c7a362e54e6bf51952015487edaa606c4e11632bc32442a9",
		Size:      139310336,
		License:   "NVIDIA Open Model License",
		Source:    transcribeCPPGGUFBase + SortformerDiarizerName + "-gguf",
	}
}

// SortformerManifestAsset is the download plan entry for the diarizer GGUF.
func SortformerManifestAsset() Asset {
	a := SortformerDiarizerAsset()
	return Asset{
		ID:       "speaker.diarizer.sortformer." + a.Name,
		Provider: STTProviderTranscribeCPP,
		Kind:     AssetKindSpeaker,
		Name:     a.Name,
		Files:    []AssetFile{{Path: a.RelPath(), URL: a.URL, SHA256: a.SHA256, Size: a.Size}},
		License:  a.License,
		Source:   a.Source,
	}
}
