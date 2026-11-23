package detect

import (
	"math"
	"testing"

	"github.com/zserge/microwakeword"

	"github.com/zserge/microwakeword/audiofrontend"

	"github.com/HuskerMinion/techo5/echod/internal/feature/detect/assets"
	"github.com/HuskerMinion/techo5/echod/internal/lib/mwwfront"
	"github.com/HuskerMinion/techo5/echod/internal/lib/wake"
)

// speechish is a few seconds of 16 kHz audio with words' worth of change in it: a gliding tone under
// noise, in bursts. What it sounds like does not matter; that the models' scores move does.
func speechish(seconds int) []int16 {
	out := make([]int16, 16000*seconds)
	seed := uint32(1)
	for i := range out {
		seed = seed*1664525 + 1013904223
		noise := float64(int32(seed)>>20) / 2048
		t := float64(i) / 16000
		burst := 0.0
		if math.Mod(t, 0.8) < 0.45 {
			burst = 1
		}
		tone := math.Sin(2 * math.Pi * (300 + 400*math.Sin(3*t)) * t)
		out[i] = int16(6000*burst*tone + 800*noise)
	}
	return out
}

// Sharing the front end must not change a single score: each model fed the shared features scores
// exactly what its own detector, reading the audio itself, scores on the same frames.
func TestSharedFrontEndScoresExactlyAsAlone(t *testing.T) {
	path, err := assets.Stop(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	models := []wake.Model{
		{ID: "a", Kind: wake.KindMicroWakeWord, Path: path, Config: microwakeword.Config{ModelPath: path, SlidingWindowSize: 5, FeaturesStepMs: 10}},
		{ID: "b", Kind: wake.KindMicroWakeWord, Path: path, Config: microwakeword.Config{ModelPath: path, SlidingWindowSize: 3, FeaturesStepMs: 10}},
		{ID: "c", Kind: wake.KindMicroWakeWord, Path: path, Config: microwakeword.Config{ModelPath: path, SlidingWindowSize: 5, FeaturesStepMs: 20}},
	}

	shared := newMicroBackend(false)
	alone := map[string]*microwakeword.Detector{}
	for _, m := range models {
		if err := shared.load(m); err != nil {
			t.Fatal(err)
		}
		cfg := m.Config
		cfg.ProbabilityCutoff = neverFires
		d, err := microwakeword.NewDetector(cfg)
		if err != nil {
			t.Fatal(err)
		}
		alone[m.ID] = d
	}
	if len(shared.fronts) != 2 {
		t.Fatalf("%d front ends for two feature steps", len(shared.fronts))
	}

	audio := speechish(6)
	moved := map[string]bool{}
	for at := 0; at+160 <= len(audio); at += 160 {
		frame := audio[at : at+160]
		scores, _ := shared.feed(frame)
		for id, d := range alone {
			d.ProcessAudio(frame)
			want := d.SlidingAverage()
			if scores[id] != want {
				t.Fatalf("model %s at sample %d: shared %v, alone %v", id, at, scores[id], want)
			}
			if want > 0 {
				moved[id] = true
			}
		}
	}
	for _, m := range models {
		if !moved[m.ID] {
			t.Logf("model %s never scored above zero, so this compared zeros for it", m.ID)
		}
	}
}

// Unloading the last model on a step drops that front end; the others keep theirs.
func TestUnloadDropsAnUnusedFrontEnd(t *testing.T) {
	path, err := assets.Stop(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	b := newMicroBackend(false)
	for id, step := range map[string]int{"a": 10, "b": 10, "c": 20} {
		m := wake.Model{ID: id, Path: path, Config: microwakeword.Config{ModelPath: path, SlidingWindowSize: 5, FeaturesStepMs: step}}
		if err := b.load(m); err != nil {
			t.Fatal(err)
		}
	}
	b.unload("c")
	if _, ok := b.fronts[frontKey{20, false}]; ok || len(b.fronts) != 1 {
		t.Errorf("fronts after unloading the only 20 ms model: %v", b.fronts)
	}
	b.unload("a")
	if _, ok := b.fronts[frontKey{10, false}]; !ok {
		t.Error("the 10 ms front end went while b still uses it")
	}
}

// A wake word and the stop word over one 10 ms frame: on one front end ("shared", "shared-exact"), as
// the daemon runs the usual pair, the image's wake word on the approximate one and the stop word on the
// exact one ("wake-and-stop"), and each alone.
func BenchmarkMicroFeed(b *testing.B) {
	path, err := assets.Stop(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	cfg := microwakeword.Config{ModelPath: path, SlidingWindowSize: 5, FeaturesStepMs: 10, ProbabilityCutoff: neverFires}
	audio := speechish(4)
	frames := len(audio) / 160

	b.Run("shared", func(b *testing.B) {
		s := newMicroBackend(false)
		for _, id := range []string{"wake", "stop"} {
			if err := s.load(wake.Model{ID: id, Path: path, Config: cfg}); err != nil {
				b.Fatal(err)
			}
		}
		for i := range b.N {
			at := (i % frames) * 160
			s.feed(audio[at : at+160])
		}
	})
	b.Run("shared-exact", func(b *testing.B) {
		s := newMicroBackend(true)
		for _, id := range []string{"wake", "stop"} {
			if err := s.load(wake.Model{ID: id, Path: path, Config: cfg}); err != nil {
				b.Fatal(err)
			}
		}
		for i := range b.N {
			at := (i % frames) * 160
			s.feed(audio[at : at+160])
		}
	})
	b.Run("wake-and-stop", func(b *testing.B) {
		s := newMicroBackend(false)
		if err := s.load(wake.Model{ID: "wake", Path: path, Config: cfg}); err != nil {
			b.Fatal(err)
		}
		if err := s.load(stopModelAt(path)); err != nil {
			b.Fatal(err)
		}
		for i := range b.N {
			at := (i % frames) * 160
			s.feed(audio[at : at+160])
		}
	})
	b.Run("alone", func(b *testing.B) {
		var ds []*microwakeword.Detector
		for range 2 {
			d, err := microwakeword.NewDetector(cfg)
			if err != nil {
				b.Fatal(err)
			}
			ds = append(ds, d)
		}
		for i := range b.N {
			at := (i % frames) * 160
			for _, d := range ds {
				d.ProcessAudio(audio[at : at+160])
			}
		}
	})
}

// The exact front end is what an exact backend feeds its models: each scores what a detector handed
// mwwfront's features of the same audio directly scores, frame for frame. And it is not the old one.
func TestExactBackendFeedsTheExactFeatures(t *testing.T) {
	path, err := assets.Stop(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := microwakeword.Config{ModelPath: path, SlidingWindowSize: 5, FeaturesStepMs: 10}
	b := newMicroBackend(true)
	if err := b.load(wake.Model{ID: "stop", Path: path, Config: cfg}); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.fronts[frontKey{10, true}].fe.(*mwwfront.Frontend); !ok {
		t.Fatalf("an exact backend runs %T", b.fronts[frontKey{10, true}].fe)
	}
	cfg.ProbabilityCutoff = neverFires
	ref, err := microwakeword.NewDetector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	fe := mwwfront.New(10)
	audio := speechish(6)
	var pending []int16
	for at := 0; at+160 <= len(audio); at += 160 {
		frame := audio[at : at+160]
		scores, _ := b.feed(frame)
		pending = append(pending, frame...)
		for len(pending) >= fe.WindowSamples() {
			ref.ProcessFeatures([][]int8{features(fe.ProcessFrame(pending[:fe.WindowSamples()]))})
			pending = pending[fe.StepSamples():]
		}
		if scores["stop"] != ref.SlidingAverage() {
			t.Fatalf("at sample %d: backend %v, mwwfront fed directly %v", at, scores["stop"], ref.SlidingAverage())
		}
	}

	old := newMicroBackend(false)
	old.front(frontKey{10, false})
	if _, ok := old.fronts[frontKey{10, false}].fe.(*audiofrontend.Frontend); !ok {
		t.Fatalf("the default backend runs %T", old.fronts[frontKey{10, false}].fe)
	}
}

// BenchmarkFrontEnds is one 30 ms window's features on each front end: the cost per 10 ms step,
// without the models.
func BenchmarkFrontEnds(b *testing.B) {
	audio := speechish(4)
	for _, fe := range []struct {
		name string
		fe   featureFront
	}{
		{"zserge", audiofrontend.New(audiofrontend.DefaultConfig(10))},
		{"exact", mwwfront.New(10)},
	} {
		b.Run(fe.name, func(b *testing.B) {
			n := (len(audio) - 480) / 160
			for i := range b.N {
				at := (i % n) * 160
				fe.fe.ProcessFrame(audio[at : at+480])
			}
		})
	}
}

// A model sent from the setup page runs on the exact front end even on the default backend, next to
// the image's word on the approximate one: two front ends for one step, each model fed its own (#352).
func TestAnUploadedModelRunsOnTheExactFrontEnd(t *testing.T) {
	path, err := assets.Stop(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := microwakeword.Config{ModelPath: path, SlidingWindowSize: 5, FeaturesStepMs: 10}
	b := newMicroBackend(false)
	if err := b.load(wake.Model{ID: "image", Path: path, Config: cfg}); err != nil {
		t.Fatal(err)
	}
	if err := b.load(wake.Model{ID: "own", Path: path, Config: cfg, Uploaded: true}); err != nil {
		t.Fatal(err)
	}
	if _, ok := b.fronts[frontKey{10, true}].fe.(*mwwfront.Frontend); !ok {
		t.Fatalf("the uploaded model's front end is %T", b.fronts[frontKey{10, true}].fe)
	}
	if _, ok := b.fronts[frontKey{10, false}].fe.(*audiofrontend.Frontend); !ok {
		t.Fatalf("the image's word moved off the approximate front end: %T", b.fronts[frontKey{10, false}].fe)
	}

	// Each scores what its own front end fed directly gives: the uploaded one mwwfront's features, the
	// image's the detector's own front end, zserge's.
	cfg.ProbabilityCutoff = neverFires
	exactRef, err := microwakeword.NewDetector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	approxRef, err := microwakeword.NewDetector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	fe := mwwfront.New(10)
	audio := speechish(6)
	var pending []int16
	for at := 0; at+160 <= len(audio); at += 160 {
		frame := audio[at : at+160]
		scores, _ := b.feed(frame)
		approxRef.ProcessAudio(frame)
		pending = append(pending, frame...)
		for len(pending) >= fe.WindowSamples() {
			exactRef.ProcessFeatures([][]int8{features(fe.ProcessFrame(pending[:fe.WindowSamples()]))})
			pending = pending[fe.StepSamples():]
		}
		if scores["own"] != exactRef.SlidingAverage() || scores["image"] != approxRef.SlidingAverage() {
			t.Fatalf("at sample %d: uploaded %v (exact alone %v), image's %v (approximate alone %v)",
				at, scores["own"], exactRef.SlidingAverage(), scores["image"], approxRef.SlidingAverage())
		}
	}

	b.unload("own")
	if _, ok := b.fronts[frontKey{10, true}]; ok || len(b.fronts) != 1 {
		t.Errorf("fronts after unloading the uploaded model: %v", b.fronts)
	}
}

// A model from Home Assistant's custom_wake_words runs on the exact front end too, like an upload: the
// same file woke a Dot uploaded and stayed silent from there. The image's words stay where they were.
func TestAHomeAssistantModelRunsOnTheExactFrontEnd(t *testing.T) {
	for _, c := range []struct {
		m     wake.Model
		exact bool
		want  bool
	}{
		{wake.Model{ID: "image"}, false, false},
		{wake.Model{ID: "image"}, true, true},
		{wake.Model{ID: "own", Uploaded: true}, false, true},
		{wake.Model{ID: "ha", FromHomeAssistant: true}, false, true},
		{wake.Model{ID: "stop", Exact: true}, false, true},
	} {
		if got := exactFor(c.m, c.exact); got != c.want {
			t.Errorf("exactFor(%s, %v) = %v, want %v", c.m.ID, c.exact, got, c.want)
		}
	}
}

// approximateByDefault pins the daemon's front end switch to its default, off, for one test.
// TECHO5_WAKE_FRONTEND=exact in the environment would otherwise put every model on the exact front end
// (the replay test in hardware/mic documents it for test runs), and a test that the stop word is the
// one model there would pass or fail by the shell it ran in. The engine's backends read the switch when
// they are built (newBackend, through build), so this covers them too.
func approximateByDefault(t *testing.T) {
	t.Helper()
	was := ExactFrontEnd
	ExactFrontEnd = false
	t.Cleanup(func() { ExactFrontEnd = was })
}

// The stop word runs on the exact front end on the default backend, next to a wake word of the image's
// on the approximate one, and each scores what its own front end fed directly gives. It shares the
// exact one with a model sent from the setup page, and the front end goes with the last model on it.
func TestTheStopWordRunsOnTheExactFrontEnd(t *testing.T) {
	approximateByDefault(t)
	path, err := assets.Stop(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stop := stopModelAt(path)
	if !stop.Exact || stop.Uploaded {
		t.Fatalf("the stop word: Exact %v, Uploaded %v", stop.Exact, stop.Uploaded)
	}
	if stop.ID != "stop" || stop.Kind != wake.KindMicroWakeWord || stop.Config.FeaturesStepMs != assets.StopFeatureStep {
		t.Fatalf("the stop word: %+v", stop)
	}
	if got := FrontEnd(stop); got != "exact (mwwfront)" {
		t.Errorf("a tool would score the stop word on %s", got)
	}

	cfg := microwakeword.Config{ModelPath: path, SlidingWindowSize: 5, FeaturesStepMs: 10}
	if got := FrontEnd(wake.Model{ID: "image", Path: path, Config: cfg}); got != "approximate (zserge audiofrontend)" {
		t.Errorf("a tool would score the image's wake word on %s", got)
	}
	b := newMicroBackend(false)
	if err := b.load(wake.Model{ID: "image", Path: path, Config: cfg}); err != nil {
		t.Fatal(err)
	}
	if err := b.load(stop); err != nil {
		t.Fatal(err)
	}
	if b.step["stop"] != (frontKey{10, true}) || b.step["image"] != (frontKey{10, false}) {
		t.Fatalf("front ends: stop %+v, the image's word %+v", b.step["stop"], b.step["image"])
	}
	if _, ok := b.fronts[frontKey{10, true}].fe.(*mwwfront.Frontend); !ok {
		t.Fatalf("the stop word's front end is %T", b.fronts[frontKey{10, true}].fe)
	}
	if _, ok := b.fronts[frontKey{10, false}].fe.(*audiofrontend.Frontend); !ok {
		t.Fatalf("the image's word moved off the approximate front end: %T", b.fronts[frontKey{10, false}].fe)
	}

	cfg.ProbabilityCutoff = neverFires
	exactRef, err := microwakeword.NewDetector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	approxRef, err := microwakeword.NewDetector(cfg)
	if err != nil {
		t.Fatal(err)
	}
	fe := mwwfront.New(10)
	audio := speechish(6)
	var pending []int16
	for at := 0; at+160 <= len(audio); at += 160 {
		frame := audio[at : at+160]
		scores, _ := b.feed(frame)
		approxRef.ProcessAudio(frame)
		pending = append(pending, frame...)
		for len(pending) >= fe.WindowSamples() {
			exactRef.ProcessFeatures([][]int8{features(fe.ProcessFrame(pending[:fe.WindowSamples()]))})
			pending = pending[fe.StepSamples():]
		}
		if scores["stop"] != exactRef.SlidingAverage() || scores["image"] != approxRef.SlidingAverage() {
			t.Fatalf("at sample %d: stop %v (exact alone %v), image's %v (approximate alone %v)",
				at, scores["stop"], exactRef.SlidingAverage(), scores["image"], approxRef.SlidingAverage())
		}
	}

	// A model of one's own is on the same front end: three models, still two front ends.
	if err := b.load(wake.Model{ID: "own", Path: path, Config: cfg, Uploaded: true}); err != nil {
		t.Fatal(err)
	}
	if len(b.fronts) != 2 {
		t.Errorf("%d front ends for the image's word, the stop word and an uploaded one", len(b.fronts))
	}
	b.unload("own")
	if _, ok := b.fronts[frontKey{10, true}]; !ok {
		t.Error("the exact front end went while the stop word still uses it")
	}
	b.unload("stop")
	if _, ok := b.fronts[frontKey{10, true}]; ok || len(b.fronts) != 1 {
		t.Errorf("fronts after unloading the stop word: %v", b.fronts)
	}
}

// Through the engine, as the daemon loads them: the stop word's slot is on the exact front end and a
// wake word's is not, while the microphones are cut too (what the stop word's log line says then), and
// the stop word is back on it when they come back.
func TestEngineScoresTheStopWordOnTheExactFrontEnd(t *testing.T) {
	approximateByDefault(t)
	path, err := assets.Stop(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := microwakeword.Config{ModelPath: path, SlidingWindowSize: 5, FeaturesStepMs: 10}
	e := New(StopSlot+1, nil)
	if err := e.Use(0, wake.Model{ID: "image", Kind: wake.KindMicroWakeWord, Path: path, Config: cfg}); err != nil {
		t.Fatal(err)
	}
	if err := e.Use(StopSlot, stopModelAt(path)); err != nil {
		t.Fatal(err)
	}
	check := func(when string) {
		t.Helper()
		if got := e.FrontOf(StopSlot); got != "tflm" {
			t.Errorf("%s: the stop word is on %q, want tflm", when, got)
		}
		if got := e.FrontOf(0); got != "zserge" {
			t.Errorf("%s: the wake word is on %q, want zserge", when, got)
		}
	}
	check("loaded")
	e.Quiet(true)
	check("muted")
	e.Quiet(false)
	check("after a mute")
	if got := e.FrontOf(1); got != "" {
		t.Errorf("an empty slot is on %q", got)
	}
}

// A wake word chosen under the stop word's ID is the same detector, so the model loaded last decides the
// front end for both slots (exactFor): this is the documented behavior, not a goal, and a change to it
// should be a change to the comment too.
func TestAWakeWordUnderTheStopWordsIDSharesItsFrontEnd(t *testing.T) {
	approximateByDefault(t)
	path, err := assets.Stop(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := microwakeword.Config{ModelPath: path, SlidingWindowSize: 5, FeaturesStepMs: 10}
	e := New(StopSlot+1, nil)
	if err := e.Use(StopSlot, stopModelAt(path)); err != nil {
		t.Fatal(err)
	}
	if err := e.Use(0, wake.Model{ID: "stop", Kind: wake.KindMicroWakeWord, Path: path, Config: cfg}); err != nil {
		t.Fatal(err)
	}
	if a, b := e.FrontOf(0), e.FrontOf(StopSlot); a != "zserge" || b != "zserge" {
		t.Errorf("loaded after the stop word: the wake word is on %q, the stop word on %q", a, b)
	}
	e.Quiet(true)
	// Muted, the answer is where both come back: Quiet(false) loads the stop word's slot last.
	if a, b := e.FrontOf(0), e.FrontOf(StopSlot); a != "tflm" || b != "tflm" {
		t.Errorf("muted: the wake word is on %q, the stop word on %q", a, b)
	}
	e.Quiet(false)
	if a, b := e.FrontOf(0), e.FrontOf(StopSlot); a != "tflm" || b != "tflm" {
		t.Errorf("after a mute: the wake word is on %q, the stop word on %q", a, b)
	}
}
