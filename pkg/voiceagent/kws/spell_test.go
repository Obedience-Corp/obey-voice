package kws

import (
	"encoding/binary"
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// vocabProto encodes a sentencepiece ModelProto holding only pieces, the way
// the real file lays them out, so the reader is tested without the model.
func vocabProto(pieces map[string]float32, control ...string) []byte {
	var out []byte
	add := func(piece string, score float32, kind uint64) {
		var msg []byte
		msg = append(msg, 0x0a)
		msg = binary.AppendUvarint(msg, uint64(len(piece)))
		msg = append(msg, piece...)
		msg = append(msg, 0x15)
		msg = binary.LittleEndian.AppendUint32(msg, math.Float32bits(score))
		msg = append(msg, 0x18)
		msg = binary.AppendUvarint(msg, kind)
		out = append(out, 0x0a)
		out = binary.AppendUvarint(out, uint64(len(msg)))
		out = append(out, msg...)
	}
	for _, piece := range control {
		add(piece, 0, 3)
	}
	for piece, score := range pieces {
		add(piece, score, 1)
	}
	// A trainer_spec the reader must step over: field 2, holding one varint.
	return append(out, 0x12, 0x02, 0x18, 0x01)
}

func testSpeller(t *testing.T, pieces map[string]float32, control ...string) *speller {
	t.Helper()
	path := filepath.Join(t.TempDir(), vocabFile)
	if err := os.WriteFile(path, vocabProto(pieces, control...), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := loadSpeller(path)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSpellPicksTheHighestScoringSplit(t *testing.T) {
	s := testSpeller(t, map[string]float32{
		"▁": -3, "▁J": -9, "▁JU": -4, "J": -8, "U": -5, "N": -4, "O": -4, "NO": -20, "UN": -6,
		"▁HE": -3, "Y": -4, "H": -9, "E": -3,
	}, "<blk>", "▁JUNO")
	for name, want := range map[string]string{
		"Juno":       "▁JU N O",
		"  hey juno": "▁HE Y ▁JU N O",
		"Hey-Juno":   "▁HE Y ▁JU N O",
		"JUNE":       "▁JU N E",
	} {
		if got, err := s.spell(name); err != nil || got != want {
			t.Errorf("spell(%q) = %q, %v; want %q", name, got, err, want)
		}
	}
	// "▁JUNO" exists only as a control piece and must not be used.
	if _, err := s.spell("Jax"); !errors.Is(err, ErrUnspellable) {
		t.Errorf("a name with no split in the vocabulary: %v", err)
	}
}

func TestCheckName(t *testing.T) {
	for _, name := range []string{"Obey", "juno", "Mary-Jane", "O'Brien", "Hey Juno", " Computer "} {
		if err := CheckName(name); err != nil {
			t.Errorf("CheckName(%q) = %v", name, err)
		}
	}
	for _, name := range []string{"", "  ", "R2D2", "Zoë", "Juno!", "'", "- -", "ジュノ"} {
		if err := CheckName(name); !errors.Is(err, ErrUnspellable) {
			t.Errorf("CheckName(%q) = %v, want ErrUnspellable", name, err)
		}
	}
}

func TestLoadSpellerRejectsDamage(t *testing.T) {
	dir := t.TempDir()
	for name, raw := range map[string][]byte{
		"empty":     {},
		"truncated": {0x0a, 0x7f, 0x01},
		"no pieces": {0x12, 0x02, 0x18, 0x01},
	} {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadSpeller(path); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Reference lines from the sentencepiece library reading the same bpe.model
// (upper-cased input, hyphens as spaces). The spotter hears a name only as
// well as its line matches what the model was trained on.
func TestSpellMatchesSentencepiece(t *testing.T) {
	dir := os.Getenv("OBEY_KWS_MODEL_DIR")
	if dir == "" {
		t.Skip("set OBEY_KWS_MODEL_DIR to the extracted keyword model")
	}
	s, err := loadSpeller(filepath.Join(dir, vocabFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range [][2]string{
		{DefaultName, defaultLine}, {"Juno", "▁JU N O"}, {"Jarvis", "▁JA R VI S"}, {"Computer", "▁COMP U TER"},
		{"Alexa", "▁A LE X A"}, {"Samantha", "▁SA MAN TH A"}, {"Max", "▁MA X"}, {"O'Brien", "▁O ' B RI EN"},
		{"Hal", "▁HA L"}, {"Friday", "▁F RI DAY"}, {"Atlas", "▁AT LA S"}, {"Nova", "▁NO V A"},
		{"Athena", "▁A TH EN A"}, {"Echo", "▁E CH O"}, {"Sage", "▁S AGE"}, {"Winston", "▁W IN S T ON"},
		{"Zephyr", "▁ Z E PH Y R"}, {"Quixote", "▁QU I X O TE"}, {"Bartholomew", "▁B AR T HO LO ME W"},
		{"Xavier", "▁ X A VI ER"}, {"Mnemosyne", "▁ M NE MO S Y NE"}, {"Ok", "▁O K"}, {"Q", "▁ Q"},
		{"Siri", "▁S I RI"}, {"Cortana", "▁C OR T AN A"}, {"Gandalf", "▁GA ND AL F"},
		{"Wednesday", "▁WE D NE S DAY"}, {"Christopher", "▁ CH R IST O PH ER"}, {"Strength", "▁ST RE NG TH"},
		{"Rhythm", "▁ R H Y TH M"}, {"Squirrel", "▁S QU IR RE L"}, {"Vacuum", "▁VA C U UM"},
		{"Hey Juno", "▁HE Y ▁JU N O"}, {"Mister Fox", "▁MI S TER ▁F O X"}, {"Old  Faithful", "▁OLD ▁FA I TH FUL"},
		{"Mary-Jane", "▁MAR Y ▁JA NE"}, {"the doctor's office", "▁THE ▁DO C T OR ' S ▁OF F IC E"},
	} {
		if got, err := s.spell(tc[0]); err != nil || got != tc[1] {
			t.Errorf("spell(%q) = %q, %v; want %q", tc[0], got, err, tc[1])
		}
	}
}
