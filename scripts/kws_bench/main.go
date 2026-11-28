// kws_bench reports acoustic hits, misses and false matches. It is not a gate
// asserting perfect accuracy, and never records audio or loads user config.
//
// By default each clip gets a fresh stream, as the recogniser does for one
// finished utterance. With -live every clip goes through one stream in order,
// in microphone-sized chunks with quiet between, as the live wake word hears
// them; clips that should not match may be any length, and are also reported
// as false matches per hour.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/kws"
)

type clip struct {
	File   string `json:"file"`
	Spoken string `json:"spoken"`
	Want   bool   `json:"want_keyword"`
	// Name is which of -names the clip should report. Empty accepts any.
	Name string `json:"want_name,omitempty"`
}
type result struct {
	clip
	Condition string `json:"condition"`
	Keyword   string `json:"keyword"`
	// HeardBy is how far into the clip the hit arrived, the figure the name
	// summon gate compares with its acoustic window.
	HeardBy float64 `json:"heard_by_s,omitempty"`
	Millis  float64 `json:"detector_ms"`
}
type counts struct {
	Hits             int `json:"hits"`
	Misses           int `json:"misses"`
	FalseMatches     int `json:"false_matches"`
	CorrectNegatives int `json:"correct_negatives"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	dir := flag.String("model-dir", "", "explicit extracted KWS model directory")
	manifest := flag.String("cases", "testdata/kws/cases.json", "labeled WAV cases, relative to this manifest")
	names := flag.String("names", "", "comma-separated names to listen for (default: Obey)")
	live := flag.Bool("live", false, "one continuous stream over every clip, as the live wake word hears them")
	gap := flag.Duration("gap", 2*time.Second, "with -live, quiet between clips")
	flag.Parse()
	if *dir == "" {
		return fmt.Errorf("-model-dir is required")
	}
	raw, err := os.ReadFile(*manifest)
	if err != nil {
		return err
	}
	var cases []clip
	if err := json.Unmarshal(raw, &cases); err != nil {
		return err
	}
	var listen []string
	for _, name := range strings.Split(*names, ",") {
		if name = strings.TrimSpace(name); name != "" {
			listen = append(listen, name)
		}
	}
	if *live {
		return runLive(*dir, *manifest, cases, listen, *gap)
	}
	d, err := kws.New(*dir, listen)
	if err != nil {
		return err
	}
	defer d.Close()
	totals := map[string]*counts{"clean": {}, "10dB white noise": {}}
	var rows []result
	for _, c := range cases {
		samples, sr, err := audio.ReadWAVFloat32(filepath.Join(filepath.Dir(*manifest), c.File))
		if err != nil {
			return err
		}
		if sr != 16000 {
			return fmt.Errorf("%s must be mono 16kHz PCM16", c.File)
		}
		noisy := append([]float32(nil), samples...)
		rng := rand.New(rand.NewPCG(42, 17))
		var power float64
		for _, x := range samples {
			power += float64(x * x)
		}
		sigma := math.Sqrt(power / float64(len(samples)) / 10)
		for i := range noisy {
			noisy[i] += float32(rng.NormFloat64() * sigma)
		}
		for i, pcm := range [][]float32{samples, noisy} {
			condition := []string{"clean", "10dB white noise"}[i]
			start := time.Now()
			hit, err := d.Observe(context.Background(), pcm)
			if err != nil {
				return err
			}
			name := hit.Name
			if c.Want && c.Name != "" && !strings.EqualFold(name, c.Name) {
				name = "" // the wrong name is a miss for this clip
			}
			rows = append(rows, result{clip: c, Condition: condition, Keyword: hit.Name, HeardBy: hit.By.Seconds(), Millis: float64(time.Since(start).Microseconds()) / 1000})
			count := totals[condition]
			switch {
			case c.Want && name != "":
				count.Hits++
			case c.Want:
				count.Misses++
			case name != "":
				count.FalseMatches++
			default:
				count.CorrectNegatives++
			}
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		Counts  map[string]*counts `json:"counts"`
		Results []result           `json:"results"`
	}{totals, rows})
}

type liveResult struct {
	clip
	// Hits are the names heard while the clip and the quiet after it played.
	Hits []string `json:"hits"`
	// HeardBy is how far into the clip each hit arrived.
	HeardBy []float64 `json:"heard_by_s"`
}

// runLive plays every clip through one listener without resetting it.
func runLive(dir, manifest string, cases []clip, names []string, gap time.Duration) error {
	listener, err := kws.NewListener(dir, names)
	if err != nil {
		return err
	}
	defer listener.Close()
	const sampleRate, chunk = 16000, 320 // 20 ms capture callbacks
	var written int64
	var spent time.Duration
	write := func(samples []float32) ([]kws.Hit, error) {
		var hits []kws.Hit
		for len(samples) > 0 {
			n := min(len(samples), chunk)
			start := time.Now()
			got, err := listener.Write(samples[:n])
			spent += time.Since(start)
			if err != nil {
				return nil, err
			}
			hits = append(hits, got...)
			samples = samples[n:]
			written += int64(n)
		}
		return hits, nil
	}
	quiet := make([]float32, int(gap.Seconds()*sampleRate))
	var total counts
	var unwantedAudio float64
	var rows []liveResult
	for _, c := range cases {
		samples, sr, err := audio.ReadWAVFloat32(filepath.Join(filepath.Dir(manifest), c.File))
		if err != nil {
			return err
		}
		if sr != sampleRate {
			return fmt.Errorf("%s must be mono 16kHz PCM16", c.File)
		}
		begin := float64(written) / sampleRate
		hits, err := write(samples)
		if err != nil {
			return err
		}
		after, err := write(quiet)
		if err != nil {
			return err
		}
		row := liveResult{clip: c, Hits: []string{}, HeardBy: []float64{}}
		matched := 0
		for _, hit := range append(hits, after...) {
			row.Hits = append(row.Hits, hit.Name)
			row.HeardBy = append(row.HeardBy, math.Round((hit.By.Seconds()-begin)*100)/100)
			if c.Name == "" || strings.EqualFold(hit.Name, c.Name) {
				matched++
			}
		}
		rows = append(rows, row)
		switch {
		case c.Want && matched > 0:
			total.Hits++
		case c.Want:
			total.Misses++
		default:
			// Every hit counts: a long clip can wake the agent more than once.
			total.FalseMatches += len(row.Hits)
			if len(row.Hits) == 0 {
				total.CorrectNegatives++
			}
			unwantedAudio += float64(len(samples))/sampleRate + gap.Seconds()
		}
	}
	audioSeconds := float64(written) / sampleRate
	summary := struct {
		Counts counts `json:"counts"`
		// AudioSeconds is everything played; UnwantedAudioSeconds is the part
		// that should not have matched, which the hourly rate is taken over.
		AudioSeconds         float64 `json:"audio_s"`
		UnwantedAudioSeconds float64 `json:"unwanted_audio_s"`
		FalseMatchesPerHour  float64 `json:"false_matches_per_hour"`
		// ProcessorShare is listener time over audio time, for one thread.
		ProcessorShare float64      `json:"processor_share"`
		Results        []liveResult `json:"results"`
	}{Counts: total, AudioSeconds: audioSeconds, UnwantedAudioSeconds: unwantedAudio, ProcessorShare: spent.Seconds() / audioSeconds, Results: rows}
	if unwantedAudio > 0 {
		summary.FalseMatchesPerHour = float64(total.FalseMatches) / unwantedAudio * 3600
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(summary)
}
