package config

import (
	"compress/bzip2"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// downloadStallTimeout aborts a download whose body has gone completely quiet.
// There is deliberately no overall request deadline: model archives are hundreds
// of MB, and a fixed timeout would kill slow-but-healthy links. Connection setup
// is bounded by the transport timeouts on downloadClient instead.
const downloadStallTimeout = 60 * time.Second

// downloadClient is shared by every model download. http.DefaultClient (what a
// bare http.Get uses) has no timeouts at all, so a wedged model host could hang
// startup or render asset setup forever.
var downloadClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	},
}

// openDownload GETs url with ctx cancellation and stall protection: the request
// aborts when the caller cancels ctx or when the body delivers no bytes for
// downloadStallTimeout. The caller owns resp.Body and must Close it. name labels
// the asset in error messages.
func openDownload(ctx context.Context, url, name string) (*http.Response, error) {
	reqCtx, cancel := context.WithCancel(ctx)
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("download %s: %w", name, err)
	}
	resp, err := downloadClient.Do(req)
	if err != nil {
		cancel()
		return nil, fmt.Errorf("download %s: %w", name, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("download %s: HTTP %d for %s", name, resp.StatusCode, url)
	}
	resp.Body = &stallGuard{rc: resp.Body, timer: time.AfterFunc(downloadStallTimeout, cancel), cancel: cancel}
	return resp, nil
}

// stallGuard cancels its request when Read delivers nothing for
// downloadStallTimeout; every productive read re-arms the timer. Close releases
// the timer and the request context.
type stallGuard struct {
	rc     io.ReadCloser
	timer  *time.Timer
	cancel context.CancelFunc
}

func (g *stallGuard) Read(p []byte) (int, error) {
	n, err := g.rc.Read(p)
	if n > 0 {
		g.timer.Reset(downloadStallTimeout)
	}
	return n, err
}

func (g *stallGuard) Close() error {
	g.timer.Stop()
	g.cancel()
	return g.rc.Close()
}

// downloadAndExtractArchive downloads a tar.bz2 and extracts to dir,
// stripping the top-level directory prefix.
// downloadAndExtractArchive downloads a tar.bz2 from url and extracts it into
// dir. The archive is decompressed and handed to extractTarStream, which
// extracts into a temp dir, verifies the check files, then atomically promotes
// the result — so a partial or malicious archive never lands at dir. name labels
// the asset in error messages.
func downloadAndExtractArchive(ctx context.Context, dir, id, url, name string, checkFiles []string, sha256Hex string, onProgress func(bytes, total int64)) error {
	resp, err := openDownload(ctx, url, name)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("download %s: create target dir: %w", name, err)
	}
	tmp, err := os.CreateTemp(dir, ".archive-*.tar.bz2.part")
	if err != nil {
		return fmt.Errorf("download %s: %w", name, err)
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	if _, err := copyBodyVerified(tmp, resp.Body, resp.ContentLength, name, sha256Hex, onProgress); err != nil {
		return err
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("extract %s: %w", name, err)
	}

	names, err := extractTarStream(bzip2.NewReader(tmp), dir, name, checkFiles)
	if err != nil {
		return err
	}
	if sha256Hex != "" {
		if err := writeArchiveInstallMarkerFiles(dir, id, url, sha256Hex, checkFiles, names); err != nil {
			return fmt.Errorf("extract %s: write install marker: %w", name, err)
		}
	}
	return nil
}

// copyBodyVerified streams body into dst (hashing when sha256Hex is set),
// reporting the raw bytes-written/total against total (percentage is derived
// once, by the caller, so it can be clamped in one place), and verifies the
// checksum after the copy. It returns the byte count written. name labels the
// asset in error messages. It is the single copy/verify core shared by file
// and archive downloads.
func copyBodyVerified(dst io.Writer, body io.Reader, total int64, name, sha256Hex string, onProgress func(bytes, total int64)) (int64, error) {
	hasher := sha256.New()
	sink := dst
	if sha256Hex != "" {
		sink = io.MultiWriter(dst, hasher)
	}

	var written int64
	buf := make([]byte, 32*1024)
	for {
		n, readErr := body.Read(buf)
		if n > 0 {
			if _, werr := sink.Write(buf[:n]); werr != nil {
				return written, fmt.Errorf("download %s: %w", name, werr)
			}
			written += int64(n)
			if onProgress != nil {
				onProgress(written, total)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return written, fmt.Errorf("download %s: %w", name, readErr)
		}
	}

	if sha256Hex != "" {
		got := hex.EncodeToString(hasher.Sum(nil))
		if !strings.EqualFold(got, sha256Hex) {
			return written, fmt.Errorf("download %s: checksum mismatch, got %s want %s (if upstream republished this file, update samantha or install the model manually into the models dir)", name, got, sha256Hex)
		}
	}
	return written, nil
}

// downloadFile downloads url into path. It writes to a temp file in the target
// directory, verifies the expected size and checksum when known, and atomically
// renames into place on success. The temp file is removed on any failure, so a
// partial or corrupt download never lands at path. file names the asset in
// error messages.
func downloadFile(ctx context.Context, path, url, file string, size int64, sha256Hex string, onProgress func(bytes, total int64)) error {
	resp, err := openDownload(ctx, url, file)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("download %s: create parent dir: %w", file, err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.part")
	if err != nil {
		return fmt.Errorf("download %s: create temp file: %w", file, err)
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		if !committed {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	written, err := copyBodyVerified(tmp, resp.Body, resp.ContentLength, file, sha256Hex, onProgress)
	if err != nil {
		return err
	}
	if size > 0 && written != size {
		return fmt.Errorf("download %s: size mismatch, got %d bytes want %d", file, written, size)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("download %s: close temp file: %w", file, err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("download %s: finalize: %w", file, err)
	}
	committed = true
	return nil
}
