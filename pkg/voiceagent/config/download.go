package config

import (
	"compress/bzip2"
	"compress/gzip"
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
	// GitHub release asset API URLs need binary content negotiation. No
	// account credentials are needed for these pinned public artifacts.
	if req.URL.Host == "api.github.com" {
		req.Header.Set("Accept", "application/octet-stream")
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
	// idle overrides downloadStallTimeout when set.
	idle time.Duration
}

func (g *stallGuard) Read(p []byte) (int, error) {
	n, err := g.rc.Read(p)
	if n > 0 {
		idle := g.idle
		if idle <= 0 {
			idle = downloadStallTimeout
		}
		g.timer.Reset(idle)
	}
	return n, err
}

func (g *stallGuard) Close() error {
	g.timer.Stop()
	g.cancel()
	return g.rc.Close()
}

// downloadAndExtractArchive verifies a tar.bz2 or tar.gz download before
// extracting it with the existing path checks and atomic promotion. The
// compressed checksum is checked before any archive entry is installed.
func downloadAndExtractArchive(ctx context.Context, dir, id, url, name string, checkFiles []string, sha256Hex string, onProgress func(bytes, total int64)) error {
	resp, err := openDownload(ctx, url, name)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("download %s: create target dir: %w", name, err)
	}
	tmp, err := os.CreateTemp(dir, ".archive-*.part")
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

	var archive io.Reader = bzip2.NewReader(tmp)
	if strings.HasSuffix(url, ".tar.gz") {
		gz, err := gzip.NewReader(tmp)
		if err != nil {
			return fmt.Errorf("open gzip %s: %w", name, err)
		}
		defer gz.Close()
		archive = gz
	}
	names, err := extractTarStream(archive, dir, name, checkFiles)
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
			return written, fmt.Errorf("download %s: checksum mismatch, got %s want %s (if upstream republished this file, update obey-voice or install the model manually into the models dir)", name, got, sha256Hex)
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
	return downloadFileChecked(ctx, path, url, file, size, sha256Hex, nil, onProgress)
}

// downloadFileChecked is downloadFile with check run on the complete temp file
// before it is renamed into place, for content that has no pinned checksum.
func downloadFileChecked(ctx context.Context, path, url, file string, size int64, sha256Hex string, check func(path string) error, onProgress func(bytes, total int64)) error {
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
	if check != nil {
		if err := check(tmpName); err != nil {
			return fmt.Errorf("download %s: %w", file, err)
		}
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("download %s: finalize: %w", file, err)
	}
	committed = true
	return nil
}
