// bridge.h is the extern-C surface bridge.cc exposes over
// webrtc::AudioProcessing, so apm.go can call it through cgo without
// generating C++ bindings (cgo cannot call C++ directly).
#ifndef OBEY_VOICE_APM_BRIDGE_H_
#define OBEY_VOICE_APM_BRIDGE_H_

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef struct ApmHandle ApmHandle;

// ApmStats mirrors webrtc::AudioProcessingStats. Every field is optional in
// the underlying API (populated only once the corresponding submodule has
// produced a measurement), so each value carries a paired has_* flag rather
// than a sentinel.
typedef struct ApmStats {
  double echo_return_loss;
  int has_echo_return_loss;
  double echo_return_loss_enhancement;
  int has_echo_return_loss_enhancement;
  double divergent_filter_fraction;
  int has_divergent_filter_fraction;
  int32_t delay_median_ms;
  int has_delay_median_ms;
  int32_t delay_standard_deviation_ms;
  int has_delay_standard_deviation_ms;
  double residual_echo_likelihood;
  int has_residual_echo_likelihood;
  double residual_echo_likelihood_recent_max;
  int has_residual_echo_likelihood_recent_max;
  int32_t delay_ms;
  int has_delay_ms;
  int voice_detected; // 0/1, valid only when has_voice_detected
  int has_voice_detected;
  int32_t output_rms_dbfs;
  int has_output_rms_dbfs;
} ApmStats;

// apm_new builds an AudioProcessing instance with AEC3 (non-mobile mode)
// enabled for mono audio at sample_rate_hz, which must be one of the API's
// NativeRates (8000, 16000, 32000, 48000). The residual echo detector, voice
// detector, and level estimator are enabled too: all three are cheap and
// their stats come back through apm_get_stats for free. Noise suppression is
// enabled at ns_level (0=low, 1=moderate, 2=high, 3=very high) only when
// enable_ns is non-zero.
//
// Returns NULL if the library rejects the configuration (e.g. an
// unsupported sample rate).
ApmHandle* apm_new(int sample_rate_hz, int enable_ns, int ns_level);

// apm_frame_samples reports the number of mono int16 samples a single 10ms
// frame holds at the handle's sample rate (sample_rate_hz / 100). Every
// render/capture call below must be handed exactly this many samples.
int apm_frame_samples(const ApmHandle* h);

// apm_process_render feeds one 10ms far-end (playback reference) frame into
// the echo canceller's reverse stream. Returns 0 on success, or a negative
// webrtc::AudioProcessing::Error code.
int apm_process_render(ApmHandle* h, const int16_t* frame, int num_samples);

// apm_process_capture runs one 10ms near-end (microphone) frame through the
// full configured chain (AEC3, optional NS) and writes the result to out.
// frame and out must not overlap. Returns 0 on success, or a negative
// webrtc::AudioProcessing::Error code.
int apm_process_capture(ApmHandle* h, const int16_t* frame, int num_samples,
                         int16_t* out);

// apm_set_stream_delay_ms declares the render-to-capture delay in
// milliseconds — the time between a frame reaching apm_process_render and
// its echo reaching the microphone. Required whenever echo cancellation is
// enabled (it is, unconditionally, in apm_new); apm_process_capture also
// reasserts the last value it was given immediately before processing, so
// callers may set it once at startup rather than every frame. Returns 0 on
// success, or a negative webrtc::AudioProcessing::Error code.
int apm_set_stream_delay_ms(ApmHandle* h, int delay_ms);

// apm_get_stats fills out with the most recently produced statistics. Safe
// to call every frame; the underlying library aggregates internally and
// only updates roughly once a second.
void apm_get_stats(const ApmHandle* h, ApmStats* out);

// apm_free destroys the instance. h may be NULL.
void apm_free(ApmHandle* h);

#ifdef __cplusplus
}
#endif

#endif // OBEY_VOICE_APM_BRIDGE_H_
