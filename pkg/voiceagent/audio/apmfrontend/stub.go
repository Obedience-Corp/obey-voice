//go:build !apm

package apmfrontend

import (
	"errors"

	"github.com/Obedience-Corp/obey-voice/pkg/voiceagent/audio"
)

// ErrUnavailable is returned by New in a build without the apm tag: the
// webrtc-audio-processing shim (pkg/voiceagent/audio/apm) is excluded from
// this build entirely, so there is nothing to construct. See
// config.ValidateVoiceFrontend, which turns this into a startup error and a
// doctor diagnostic with the same explanation — D5 requires a clear
// rejection here, never a silent fallback to nlms.
var ErrUnavailable = errors.New("apmfrontend: requires a build with the apm tag against the webrtc-audio-processing library (see scripts/build-webrtc-apm.sh)")

// New always fails without the apm tag. nsLevel is accepted only to keep the
// signature identical to the tagged implementation.
func New(nsLevel int) (audio.Frontend, error) {
	return nil, ErrUnavailable
}
