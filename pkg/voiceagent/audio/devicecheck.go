package audio

/*
#include <stdlib.h>
*/
import "C"

import (
	"context"
	"fmt"
	"sync"
	"unsafe"

	"github.com/gen2brain/malgo"
)

// DeviceChecker probes audio hardware through miniaudio, the same backend used
// for capture and playback. It only enumerates devices (open/close a context);
// it never records or plays audio. enumerate is injectable for tests.
type DeviceChecker struct {
	enumerate func(kind malgo.DeviceType) ([]string, error)
}

// NewDeviceChecker returns a checker backed by the real miniaudio context.
func NewDeviceChecker() *DeviceChecker {
	return &DeviceChecker{enumerate: malgoDeviceNames}
}

// CaptureDevices returns the names of available microphone devices.
func (c *DeviceChecker) CaptureDevices(ctx context.Context) ([]string, error) {
	return c.devices(ctx, malgo.Capture)
}

// PlaybackDevices returns the names of available speaker devices.
func (c *DeviceChecker) PlaybackDevices(ctx context.Context) ([]string, error) {
	return c.devices(ctx, malgo.Playback)
}

// devices runs enumeration in a goroutine so a wedged audio backend (a
// blocking cgo call) cannot outlive ctx; the result channel is buffered so the
// goroutine never leaks.
func (c *DeviceChecker) devices(ctx context.Context, kind malgo.DeviceType) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	type result struct {
		names []string
		err   error
	}
	ch := make(chan result, 1)
	go func() {
		names, err := c.enumerate(kind)
		ch <- result{names, err}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-ch:
		return r.names, r.err
	}
}

func malgoDeviceNames(kind malgo.DeviceType) ([]string, error) {
	mctx, err := malgo.InitContext(nil, malgo.ContextConfig{}, nil)
	if err != nil {
		return nil, fmt.Errorf("init audio context: %w", err)
	}
	defer func() {
		_ = mctx.Uninit()
		mctx.Free()
	}()

	infos, err := mctx.Devices(kind)
	if err != nil {
		return nil, fmt.Errorf("enumerate devices: %w", err)
	}
	names := make([]string, 0, len(infos))
	for i := range infos {
		names = append(names, infos[i].Name())
	}
	return names, nil
}

// selectDevice configures sub with the named device from ctx and returns a
// release function for the temporary C-owned device ID. The caller must keep
// the allocation alive until malgo.InitDevice returns; miniaudio copies the ID
// during initialization.
//
// An empty name deliberately leaves DeviceID nil so miniaudio follows the
// operating-system default. Device names are used in config because miniaudio
// IDs are backend-specific and can change across boots; a missing configured
// name is reported instead of silently routing private audio to a different
// device.
func selectDevice(ctx malgo.Context, kind malgo.DeviceType, name string, sub *malgo.SubConfig) (func(), error) {
	if name == "" {
		return func() {}, nil
	}
	infos, err := ctx.Devices(kind)
	if err != nil {
		return nil, fmt.Errorf("enumerate devices: %w", err)
	}
	for i := range infos {
		if infos[i].Name() == name {
			ptr, release := copyDeviceIDToC(infos[i].ID)
			sub.DeviceID = ptr
			return release, nil
		}
	}
	return nil, fmt.Errorf("audio device %q is not available", name)
}

// copyDeviceIDToC detaches a miniaudio device ID from DeviceInfo's Go-managed
// storage. DeviceInfo also owns a Formats slice, so passing &info.ID[0] through
// malgo's C device config violates cgo's pointer rules and panics at runtime
// with "Go pointer to unpinned Go pointer". In the pinned malgo version,
// DeviceID.Pointer uses C.CBytes, whose allocation must be released with C.free.
func copyDeviceIDToC(id malgo.DeviceID) (unsafe.Pointer, func()) {
	ptr := id.Pointer()
	var once sync.Once
	return ptr, func() {
		once.Do(func() {
			C.free(ptr)
		})
	}
}

// SelectPlaybackDevice points sub at the named playback device and returns the
// release func that frees the C copy of its ID.
//
// It is the only device selector this package exports: capture calls
// selectDevice in-package, and the playback package needs exactly this one
// operation. The returned func must run once the device is closed — the ID is a
// C allocation, so skipping it leaks.
func SelectPlaybackDevice(ctx malgo.Context, name string, sub *malgo.SubConfig) (func(), error) {
	return selectDevice(ctx, malgo.Playback, name, sub)
}
