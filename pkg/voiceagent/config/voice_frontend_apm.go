//go:build apm

package config

// WebRTCFrontendBuildAvailable reports whether this binary was built with the
// apm tag — i.e. whether voice_frontend: webrtc can actually construct a
// front-end. This mirrors onnxrt's cgo/!cgo split (pkg/voiceagent/onnxrt):
// same package, two build-tagged files, so callers in a package that cannot
// itself depend on the apm-tagged shim (import-cycle risk: audio already
// imports config) still get a straight answer.
func WebRTCFrontendBuildAvailable() bool { return true }
