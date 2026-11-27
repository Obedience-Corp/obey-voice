// kws_bench reports acoustic hits, misses and false matches. It is not a gate
// asserting perfect accuracy, and never records audio or loads user config.
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
