// A stand-in for WebRTC's audio_processing.h, for tools/test_aec_helper.py only: just the part of the
// API techo5-aec.cpp uses, with a canceller that subtracts the reference it was last given from the
// microphone, sample for sample, and does nothing else. It lets the helper's own work (its arguments,
// which signal the delay holds back, one block out for every block in) be tested on any machine with
// a C++ compiler, without the library. What the real canceller does to the sound is the library's, and
// is not tested here.
#pragma once

#include <algorithm>
#include <cstddef>
#include <cstdint>
#include <cstring>

namespace webrtc {

class StreamConfig {
public:
	StreamConfig(int rate, size_t channels, bool) : rate_(rate), channels_(channels) {}
	size_t num_frames() const { return static_cast<size_t>(rate_ / 100); }
	size_t num_channels() const { return channels_; }

private:
	int rate_;
	size_t channels_;
};

class AudioProcessing {
public:
	struct Config {
		struct {
			bool enabled = false;
			bool mobile_mode = false;
		} echo_canceller;
		struct {
			bool enabled = false;
		} high_pass_filter;
		struct NoiseSuppression {
			enum Level { kLow, kModerate, kHigh, kVeryHigh };
			bool enabled = false;
			Level level = kModerate;
		} noise_suppression;
		struct {
			bool enabled = false;
			struct {
				float gain_db = 0;
			} fixed_digital;
		} gain_controller2;
	};

	void ApplyConfig(const Config&) {}
	int set_stream_delay_ms(int) { return 0; }

	int ProcessReverseStream(const int16_t* src, const StreamConfig& in, const StreamConfig&, int16_t* dest) {
		size_t n = std::min(in.num_frames() * in.num_channels(), kMax);
		std::memcpy(far_, src, n * sizeof(int16_t));
		if (dest != src) std::memmove(dest, src, n * sizeof(int16_t));
		return 0;
	}

	// The microphone less the reference, so the output says what each of them was when they met.
	int ProcessStream(const int16_t* src, const StreamConfig& in, const StreamConfig&, int16_t* dest) {
		size_t n = std::min(in.num_frames() * in.num_channels(), kMax);
		for (size_t i = 0; i < n; i++) {
			int v = int(src[i]) - int(far_[i]);
			dest[i] = int16_t(std::clamp(v, -32768, 32767));
		}
		return 0;
	}

private:
	static constexpr size_t kMax = 480;  // 10 ms at 48 kHz, the most one call carries
	int16_t far_[kMax] = {};
};

class AudioProcessingBuilder {
public:
	AudioProcessing* Create() { return new AudioProcessing(); }
};

}  // namespace webrtc
