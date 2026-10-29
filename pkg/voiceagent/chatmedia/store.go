// Package chatmedia owns validated, durable chat attachments shared by clients.
package chatmedia

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/draw"
	_ "image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/config"
)

const MaxBytes = 8 << 20
const MaxPixels = 16 << 20
const MaxAttachments = 4

type Ref struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Format string `json:"format"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Frames int    `json:"frames"`
}
type Store struct{ Dir string }

func DefaultStore() Store { return Store{Dir: filepath.Join(config.SessionsDir(), "attachments")} }
func (s Store) Path(r Ref) string {
	id, err := hex.DecodeString(r.ID)
	if err != nil || len(id) != 32 || (r.Format != "png" && r.Format != "jpeg" && r.Format != "gif") {
		return ""
	}
	return filepath.Join(s.Dir, r.ID+"."+r.Format)
}
func (s Store) ImportFile(path string) (Ref, error) {
	f, err := os.Open(path)
	if err != nil {
		return Ref{}, fmt.Errorf("open attachment: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Ref{}, err
	}
	if !st.Mode().IsRegular() {
		return Ref{}, errors.New("attachment must be a regular file")
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return Ref{}, err
	}
	return s.Import(filepath.Base(path), data)
}
func (s Store) Import(name string, data []byte) (Ref, error) {
	r, err := Inspect(name, data)
	if err != nil {
		return Ref{}, err
	}
	hash := sha256.Sum256(data)
	r.ID = hex.EncodeToString(hash[:])
	if err = os.MkdirAll(s.Dir, 0700); err != nil {
		return Ref{}, err
	}
	f, err := os.CreateTemp(s.Dir, ".import-*")
	if err != nil {
		return Ref{}, err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), s.Path(r))
	}
	if err != nil {
		return Ref{}, fmt.Errorf("save attachment: %w", err)
	}
	return r, nil
}
func Inspect(name string, data []byte) (Ref, error) {
	if len(data) == 0 || len(data) > MaxBytes {
		return Ref{}, errors.New("image must be between 1 byte and 8 MiB")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Ref{}, fmt.Errorf("decode image: %w", err)
	}
	if format != "png" && format != "jpeg" && format != "gif" {
		return Ref{}, errors.New("use a PNG, JPEG or GIF image")
	}
	if cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 4096 || cfg.Height > 4096 || int64(cfg.Width)*int64(cfg.Height) > MaxPixels {
		return Ref{}, errors.New("image exceeds 4096 pixels per side or 16 megapixels")
	}
	frames := 1
	if format == "gif" {
		frames, err = checkGIF(data)
		if err != nil {
			return Ref{}, err
		}
	}
	// Validate the complete image before accepting it into durable history.
	if _, _, err = image.Decode(bytes.NewReader(data)); err != nil {
		return Ref{}, fmt.Errorf("decode image: %w", err)
	}
	name = filepath.Base(name)
	if name == "." || name == "" {
		name = "clipboard." + format
	}
	return Ref{Name: name, Format: format, Width: cfg.Width, Height: cfg.Height, Frames: frames}, nil
}
func (s Store) Read(r Ref) ([]byte, error) {
	path := s.Path(r)
	if path == "" {
		return nil, errors.New("invalid attachment ID")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("attachment %s: %w", r.Name, err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxBytes+1))
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	if len(data) > MaxBytes || hex.EncodeToString(sum[:]) != r.ID {
		return nil, errors.New("attachment content has changed")
	}
	return data, nil
}

// ModelImage uses the first GIF frame; original animation stays in the store.
func (s Store) ModelImage(r Ref) ([]byte, error) {
	data, err := s.Read(r)
	if err != nil {
		return nil, err
	}
	if r.Format != "gif" {
		return data, nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	canvas := image.NewRGBA(image.Rect(0, 0, cfg.Width, cfg.Height))
	draw.Draw(canvas, img.Bounds(), img, img.Bounds().Min, draw.Over)
	var out bytes.Buffer
	err = png.Encode(&out, canvas)
	return out.Bytes(), err
}
