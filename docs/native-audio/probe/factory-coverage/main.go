// Command factory-coverage verifies forbidden native-persona factory execution
// from a real Go atomic coverage profile and its exact committed source receipt.
package main

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

type target struct{ File, Function string }

var targets = []target{
	{"pkg/voiceagent/stt/factory.go", "NewProvider"},
	{"pkg/voiceagent/stt/parakeet.go", "NewParakeetSTT"},
	{"pkg/voiceagent/stt/whispercpp.go", "NewWhisperCPPSTT"},
	{"pkg/voiceagent/stt/sherpa.go", "NewSherpaOfflineSTT"},
	{"pkg/voiceagent/stt/sherpa_streaming.go", "NewSherpaStreamingSTT"},
	{"pkg/voiceagent/stt/transcribecpp.go", "NewTranscribeCPPSTT"},
	{"pkg/voiceagent/tts/providers/providers.go", "NewProvider"},
	{"pkg/voiceagent/tts/factory.go", "NewKokoroProvider"},
	{"pkg/voiceagent/tts/kokoro.go", "NewKokoroTTS"},
	{"pkg/voiceagent/tts/qwen/qwen.go", "NewQwen3TTS"},
	{"pkg/voiceagent/phonon/install.go", "Ensure"},
	{"pkg/voiceagent/phonon/process.go", "Start"},
}

type block struct {
	file       string
	start, end int
	count      uint64
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	root := flag.String("root", ".", "source repository")
	profile := flag.String("profile", "", "Go atomic coverage profile")
	receipt := flag.String("receipt", "", "native ordinary-agent JSON receipt")
	output := flag.String("out", "", "fresh output JSON path")
	flag.Parse()
	if *profile == "" || *receipt == "" || !filepath.IsAbs(*output) {
		return errors.New("provide profile, receipt and absolute fresh output path")
	}
	raw, err := os.ReadFile(*receipt)
	if err != nil {
		return err
	}
	var proof struct {
		Revision string `json:"checkout_revision"`
		Passed   bool   `json:"test_pass"`
	}
	if err := json.Unmarshal(raw, &proof); err != nil {
		return err
	}
	decoded, err := hex.DecodeString(proof.Revision)
	if err != nil || len(decoded) != 20 || !proof.Passed {
		return errors.New("receipt must identify a passing exact committed trial")
	}
	coverage, err := os.ReadFile(*profile)
	if err != nil {
		return err
	}
	blocks, err := parseCoverage(coverage)
	if err != nil {
		return err
	}
	rows := []map[string]any{}
	for _, t := range targets {
		source, err := exec.Command("git", "-C", *root, "show", proof.Revision+":"+t.File).Output()
		if err != nil {
			return fmt.Errorf("read committed %s: %w", t.File, err)
		}
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, t.File, source, 0)
		if err != nil {
			return err
		}
		first, last := 0, 0
		for _, decl := range parsed.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == t.Function {
				first = fset.Position(fn.Pos()).Line
				last = fset.Position(fn.End()).Line
			}
		}
		if first == 0 {
			return fmt.Errorf("missing committed factory %s:%s", t.File, t.Function)
		}
		count := 0
		var executed uint64
		for _, b := range blocks {
			if strings.HasSuffix(b.file, "/"+t.File) && b.start >= first && b.end <= last {
				count++
				executed += b.count
			}
		}
		if count == 0 {
			return fmt.Errorf("factory lacks coverage instrumentation: %s:%s", t.File, t.Function)
		}
		if executed != 0 {
			return fmt.Errorf("forbidden factory executed: %s:%s (%d block executions)", t.File, t.Function, executed)
		}
		sum := sha256.Sum256(source)
		rows = append(rows, map[string]any{"file": t.File, "function": t.Function, "instrumented_blocks": count, "executed_blocks": executed, "source_sha256": hex.EncodeToString(sum[:])})
	}
	proofHash, profileHash := sha256.Sum256(raw), sha256.Sum256(coverage)
	report := map[string]any{"checkout_revision": proof.Revision, "receipt_sha256": hex.EncodeToString(proofHash[:]), "coverage_sha256": hex.EncodeToString(profileHash[:]), "factories": rows, "forbidden_factory_execution": false, "meaning": "all instrumented statement counters in each actual constructor body are zero; no audible/device or general-quality claim"}
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(*output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}
func parseCoverage(data []byte) ([]block, error) {
	scan := bufio.NewScanner(strings.NewReader(string(data)))
	if !scan.Scan() || scan.Text() != "mode: atomic" {
		return nil, errors.New("expected atomic coverage profile")
	}
	blocks := []block{}
	for scan.Scan() {
		fields := strings.Fields(scan.Text())
		if len(fields) != 3 {
			return nil, errors.New("malformed coverage row")
		}
		separator := strings.LastIndexByte(fields[0], ':')
		if separator < 0 {
			return nil, errors.New("missing coverage position")
		}
		position := fields[0][separator+1:]
		var start, col, end, endCol int
		if n, err := fmt.Sscanf(position, "%d.%d,%d.%d", &start, &col, &end, &endCol); err != nil || n != 4 {
			return nil, errors.New("invalid coverage position")
		}
		count, err := strconv.ParseUint(fields[2], 10, 64)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, block{file: fields[0][:separator], start: start, end: end, count: count})
	}
	if err := scan.Err(); err != nil {
		return nil, err
	}
	return blocks, nil
}
