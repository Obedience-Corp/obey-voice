// bridge.cc implements bridge.h over webrtc::AudioProcessing. It is the only
// place in this package that touches the C++ API; apm.go never sees a C++
// type.
#include "bridge.h"

#include <memory>
#include <utility>

#include "api/audio/echo_canceller3_config.h"
#include "modules/audio_processing/include/audio_processing.h"

namespace {

using webrtc::AudioProcessing;
using webrtc::AudioProcessingBuilder;
using webrtc::AudioProcessingStats;
using webrtc::StreamConfig;

// NoiseSuppressionLevel maps the small int the Go side passes (0..3) onto the
// library's enum, clamping anything out of range to kModerate rather than
// letting an invalid cast reach ApplyConfig.
AudioProcessing::Config::NoiseSuppression::Level NoiseSuppressionLevel(
    int level) {
  switch (level) {
    case 0:
      return AudioProcessing::Config::NoiseSuppression::kLow;
    case 2:
      return AudioProcessing::Config::NoiseSuppression::kHigh;
    case 3:
      return AudioProcessing::Config::NoiseSuppression::kVeryHigh;
    default:
      return AudioProcessing::Config::NoiseSuppression::kModerate;
  }
}

}  // namespace

struct ApmHandle {
  rtc::scoped_refptr<AudioProcessing> apm;
  StreamConfig stream_config;
  int frame_samples = 0;
  int stream_delay_ms = 0;
};

ApmHandle* apm_new(int sample_rate_hz, int enable_ns, int ns_level) {
  rtc::scoped_refptr<AudioProcessing> apm = AudioProcessingBuilder().Create();
  if (!apm) {
    return nullptr;
  }

  AudioProcessing::Config config;
  // AEC3, desktop (non-mobile) mode — see 03-candidates.md A1. mobile_mode
  // selects AEC3's lower-complexity tuning rather than a different filter;
  // there is no separate legacy AECM path left in this library version.
  config.echo_canceller.enabled = true;
  config.echo_canceller.mobile_mode = false;
  // AEC3 assumes a high-pass-filtered capture signal; enforce_high_pass_filtering
  // defaults to true, but enabling the filter explicitly avoids depending on
  // that default silently changing upstream.
  config.high_pass_filter.enabled = true;
  // Cheap stats the harness and any future runtime caller can read for free
  // via apm_get_stats: residual_echo_detector is already on by default, but
  // set it explicitly so that stays true regardless of upstream defaults.
  config.residual_echo_detector.enabled = true;
  config.voice_detection.enabled = true;
  config.level_estimation.enabled = true;
  if (enable_ns) {
    config.noise_suppression.enabled = true;
    config.noise_suppression.level = NoiseSuppressionLevel(ns_level);
  }
  apm->ApplyConfig(config);

  auto* h = new ApmHandle();
  h->apm = std::move(apm);
  h->stream_config = StreamConfig(sample_rate_hz, /*num_channels=*/1,
                                   /*has_keyboard=*/false);
  h->frame_samples = sample_rate_hz / 100;
  return h;
}

int apm_frame_samples(const ApmHandle* h) {
  if (!h) {
    return 0;
  }
  return h->frame_samples;
}

int apm_process_render(ApmHandle* h, const int16_t* frame, int num_samples) {
  if (!h || !frame || num_samples != h->frame_samples) {
    return AudioProcessing::kBadDataLengthError;
  }
  // src and dest may share memory (documented in audio_processing.h); the
  // reverse-stream output is not needed here, so reuse the input buffer.
  return h->apm->ProcessReverseStream(frame, h->stream_config,
                                       h->stream_config,
                                       const_cast<int16_t*>(frame));
}

int apm_process_capture(ApmHandle* h, const int16_t* frame, int num_samples,
                         int16_t* out) {
  if (!h || !frame || !out || num_samples != h->frame_samples) {
    return AudioProcessing::kBadDataLengthError;
  }
  // Reasserted every frame per the documented calling convention (see the
  // usage example in audio_processing.h): set_stream_delay_ms is a
  // per-ProcessStream parameter, even though callers here typically only
  // change the value itself on a delay-estimate update.
  h->apm->set_stream_delay_ms(h->stream_delay_ms);
  return h->apm->ProcessStream(frame, h->stream_config, h->stream_config,
                                out);
}

int apm_set_stream_delay_ms(ApmHandle* h, int delay_ms) {
  if (!h) {
    return AudioProcessing::kNullPointerError;
  }
  h->stream_delay_ms = delay_ms;
  return h->apm->set_stream_delay_ms(delay_ms);
}

void apm_get_stats(const ApmHandle* h, ApmStats* out) {
  if (!out) {
    return;
  }
  *out = ApmStats{};
  if (!h) {
    return;
  }

  // GetStatistics() is non-const in the API even though it only reads
  // aggregated state; the handle itself is logically read-only here.
  AudioProcessingStats stats =
      const_cast<AudioProcessing*>(h->apm.get())->GetStatistics();

  if (stats.echo_return_loss) {
    out->echo_return_loss = *stats.echo_return_loss;
    out->has_echo_return_loss = 1;
  }
  if (stats.echo_return_loss_enhancement) {
    out->echo_return_loss_enhancement = *stats.echo_return_loss_enhancement;
    out->has_echo_return_loss_enhancement = 1;
  }
  if (stats.divergent_filter_fraction) {
    out->divergent_filter_fraction = *stats.divergent_filter_fraction;
    out->has_divergent_filter_fraction = 1;
  }
  if (stats.delay_median_ms) {
    out->delay_median_ms = *stats.delay_median_ms;
    out->has_delay_median_ms = 1;
  }
  if (stats.delay_standard_deviation_ms) {
    out->delay_standard_deviation_ms = *stats.delay_standard_deviation_ms;
    out->has_delay_standard_deviation_ms = 1;
  }
  if (stats.residual_echo_likelihood) {
    out->residual_echo_likelihood = *stats.residual_echo_likelihood;
    out->has_residual_echo_likelihood = 1;
  }
  if (stats.residual_echo_likelihood_recent_max) {
    out->residual_echo_likelihood_recent_max =
        *stats.residual_echo_likelihood_recent_max;
    out->has_residual_echo_likelihood_recent_max = 1;
  }
  if (stats.delay_ms) {
    out->delay_ms = *stats.delay_ms;
    out->has_delay_ms = 1;
  }
  if (stats.voice_detected) {
    out->voice_detected = *stats.voice_detected ? 1 : 0;
    out->has_voice_detected = 1;
  }
  if (stats.output_rms_dbfs) {
    out->output_rms_dbfs = *stats.output_rms_dbfs;
    out->has_output_rms_dbfs = 1;
  }
}

void apm_free(ApmHandle* h) { delete h; }
