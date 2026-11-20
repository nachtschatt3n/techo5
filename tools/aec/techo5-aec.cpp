// techo5-aec — WebRTC's echo canceller as a small helper the daemon talks to over pipes.
//
// The daemon is static Go with no C in it; WebRTC's AudioProcessing is C++ and Alpine ships it
// (webrtc-audio-processing-1). So this process sits between: it reads 20 ms blocks from stdin —
// 320 samples of microphone, then 320 samples of the playback loopback, int16 little-endian at
// 16 kHz — runs the far end through the canceller's reverse stream and the microphone through the
// forward one, and writes the 320 processed samples to stdout. One block in, one block out, in
// order, so the daemon's read blocks for exactly as long as the processing takes.
//
// The loopback is sample aligned with the microphones (the FPGA delivers them in one frame), so the
// reported stream delay is 0 and the plain filter converges; see docs/porting-plan.md, "Echo
// cancellation, WebRTC grade", for the measurements this follows.
//
// Sample aligned is not the same as the echo arriving late enough, though. The loopback is what went
// to the speaker, and its echo reaches the microphones only after what lies between: the amplifier,
// the speaker and a few centimeters of air, which in an enclosure this size may add up to very
// little. Offline, with this library, AEC3's linear filter did not converge when the echo trailed
// the reference by less than about 32 samples (2 ms), and the suppressor was left to do the work,
// which takes a voice over the reply down with the echo. --delay-ms holds the microphone back by
// that many milliseconds before the canceller sees it, so the reference always leads by at least
// that much, as the Home Assistant Voice PE's audio chip does with its 40 ms. It is a plain delay
// line: every block still comes out as one block, in order, only N ms later than it went in, so the
// daemon's timing is untouched. 0, the default, is no delay at all, which is how the helper always
// ran. How short the lag really is on a device has not been measured.
//
// The daemon feeds the helper only while something plays, so blocks stop for as long as nothing
// does. After such a gap the line is emptied, so a new run starts from silence rather than from the
// last few milliseconds of the run before, however long ago that was.
//
//   techo5-aec [--aec low|moderate|high] [--ns off|low|moderate|high|veryhigh] [--gain-db N]
//              [--delay-ms 0-100]
//
// Build: tools/linux/build-aec.sh (in WSL, armv7 Alpine under QEMU).

#include <algorithm>
#include <cerrno>
#include <chrono>
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <memory>
#include <string>
#include <unistd.h>
#include <vector>

#include <modules/audio_processing/include/audio_processing.h>

namespace {

const int kRate = 16000;
const int kBlock = 320;           // 20 ms, the daemon's frame
const int kChunk = kRate / 100;   // 10 ms, WebRTC's frame

// kMaxDelayMs is the most --delay-ms takes. The daemon offers up to 20; past that the delay starts to
// be heard as lateness by everything that listens, and AEC3 looks for the echo over a far longer span
// on its own, so there is nothing a longer hold could fix.
const int kMaxDelayMs = 100;

// kGap is how long the daemon has to go without sending a block for the delay line to be emptied.
// Blocks come every 20 ms while something plays, so this is ten of them missing: a run that ended,
// not a late block.
const auto kGap = std::chrono::milliseconds(200);

// Delay holds a signal back by a fixed number of samples: what comes out of apply is what went in
// that many samples earlier, and the line starts out silent. A ring, so a block in is always exactly
// a block out and nothing is dropped or repeated however the blocks fall against the delay.
class Delay {
public:
	explicit Delay(int samples) : line_(samples, 0) {}

	// clear makes the line silent again, as it starts out.
	void clear() {
		std::fill(line_.begin(), line_.end(), 0);
		pos_ = 0;
	}

	// apply swaps each sample of block for the one held longest, and keeps the new one in its place.
	void apply(int16_t* block, int n) {
		if (line_.empty()) return;
		for (int i = 0; i < n; i++) {
			int16_t in = block[i];
			block[i] = line_[pos_];
			line_[pos_] = in;
			if (++pos_ == line_.size()) pos_ = 0;
		}
	}

private:
	std::vector<int16_t> line_;
	size_t pos_ = 0;
};

bool readAll(int fd, void* buf, size_t n) {
	auto* p = static_cast<uint8_t*>(buf);
	while (n > 0) {
		ssize_t r = read(fd, p, n);
		if (r == 0) return false;
		if (r < 0) {
			if (errno == EINTR) continue;
			return false;
		}
		p += r;
		n -= r;
	}
	return true;
}

bool writeAll(int fd, const void* buf, size_t n) {
	auto* p = static_cast<const uint8_t*>(buf);
	while (n > 0) {
		ssize_t w = write(fd, p, n);
		if (w < 0) {
			if (errno == EINTR) continue;
			return false;
		}
		p += w;
		n -= w;
	}
	return true;
}

}  // namespace

int main(int argc, char** argv) {
	std::string aec = "low", ns = "low";
	float gainDb = 0;
	int delayMs = 0;
	for (int i = 1; i < argc; i++) {
		std::string a = argv[i];
		if (a == "--aec" && i + 1 < argc) aec = argv[++i];
		else if (a == "--ns" && i + 1 < argc) ns = argv[++i];
		else if (a == "--gain-db" && i + 1 < argc) gainDb = std::atof(argv[++i]);
		else if (a == "--delay-ms" && i + 1 < argc) {
			// Strictly a whole number in range: a delay that is quietly taken for 0, or for another
			// number, would look like a setting that does nothing.
			const char* v = argv[++i];
			char* end = nullptr;
			long ms = std::strtol(v, &end, 10);
			if (end == v || *end != '\0' || ms < 0 || ms > kMaxDelayMs) {
				std::fprintf(stderr, "techo5-aec: --delay-ms takes 0 to %d, not %s\n", kMaxDelayMs, v);
				return 2;
			}
			delayMs = static_cast<int>(ms);
		} else {
			std::fprintf(stderr, "techo5-aec: unknown argument %s\n", argv[i]);
			return 2;
		}
	}

	std::unique_ptr<webrtc::AudioProcessing> apm(webrtc::AudioProcessingBuilder().Create());
	if (!apm) {
		std::fprintf(stderr, "techo5-aec: AudioProcessing failed to build\n");
		return 1;
	}
	webrtc::AudioProcessing::Config cfg;
	cfg.echo_canceller.enabled = true;
	cfg.echo_canceller.mobile_mode = false;  // the full linear canceller, not the mobile suppressor
	cfg.high_pass_filter.enabled = true;
	cfg.noise_suppression.enabled = ns != "off";
	if (ns == "low") cfg.noise_suppression.level = webrtc::AudioProcessing::Config::NoiseSuppression::kLow;
	else if (ns == "moderate") cfg.noise_suppression.level = webrtc::AudioProcessing::Config::NoiseSuppression::kModerate;
	else if (ns == "high") cfg.noise_suppression.level = webrtc::AudioProcessing::Config::NoiseSuppression::kHigh;
	else if (ns == "veryhigh") cfg.noise_suppression.level = webrtc::AudioProcessing::Config::NoiseSuppression::kVeryHigh;
	// A digital gain after cancellation, where a boost belongs: an analog boost clips the echo
	// before the canceller sees it.
	cfg.gain_controller2.enabled = gainDb != 0;
	cfg.gain_controller2.fixed_digital.gain_db = gainDb;
	apm->ApplyConfig(cfg);
	// Still 0 with a --delay-ms: the reported delay tells the canceller about a lag it cannot see in
	// the signals, and the delay here is in the signals. AEC3 finds where the echo sits in the
	// reference on its own, and that is what the pre-delay moves.
	apm->set_stream_delay_ms(0);
	(void)aec;  // suppression level is fixed at the library's default in this API version

	webrtc::StreamConfig sc(kRate, 1, false);
	int16_t mic[kBlock], far[kBlock];
	int16_t out[kBlock];
	Delay micDelay(delayMs * kRate / 1000);
	auto last = std::chrono::steady_clock::now();
	std::fprintf(stderr, "techo5-aec: ready (ns=%s gain=%.0f dB delay=%d ms)\n", ns.c_str(), gainDb, delayMs);
	while (readAll(0, mic, sizeof mic) && readAll(0, far, sizeof far)) {
		// What the line holds from before a gap is the end of another run: it would come out at the
		// start of this one as microphone sound from back then.
		auto now = std::chrono::steady_clock::now();
		if (now - last > kGap) micDelay.clear();
		last = now;
		// The microphone is held back, never the reference: the reference has to reach the canceller
		// first, and the far end is what it predicts the echo from.
		micDelay.apply(mic, kBlock);
		for (int off = 0; off < kBlock; off += kChunk) {
			// The far end first, so the forward pass sees what just played.
			apm->ProcessReverseStream(far + off, sc, sc, far + off);
			apm->set_stream_delay_ms(0);
			apm->ProcessStream(mic + off, sc, sc, out + off);
		}
		if (!writeAll(1, out, sizeof out)) break;
	}
	return 0;
}
