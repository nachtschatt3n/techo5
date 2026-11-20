package detect

import "time"

// Telling the turn that somebody may be talking over its reply (voice.TalkedOver).
//
// The built-in echo canceller learns while a reply plays, and must not learn from somebody's voice: it
// would take the voice for echo it predicted badly and fit itself to it. Nothing in the reply's audio
// says whether somebody is talking over it, but the words the device listens for do. The stop word and
// the wake words are what people say over a reply, so a detection of either, a near miss of either,
// and a wake worth asking Home Assistant about all end the learning for the rest of the turn.
//
// The command words are left out: they listen only while music plays, and their near misses are mostly
// the music (the ducker leaves them out for the same reason). So is the hang-up word, which listens
// only during a call, when the reply does not learn at all.

// talkingOver is whether a word in slot heard, nearly heard or asked about says somebody may be
// talking.
func talkingOver(slot int) bool { return slot == StopSlot || isWakeSlot(slot) }

// watchTalkingOver tells told about every word on e that talkingOver counts, with when it was said.
// It wraps the hooks already set on e, so it goes after them.
//
// A detection and a candidate are told first, before the hook routes them: a wake word may wait on
// the other devices for a moment before its turn opens (feature/nearest), and whoever said it goes on
// talking meanwhile. A near miss is told after whatever already listened for near misses (the ducker,
// the clips), which still hears every one of them, the command words' included. It arrives a little
// more than NearMissSettle after its peak, by up to a score and however long its goroutine waited, so
// it is told with when it peaked, which is when it was said: the turn tells a word said in the request
// itself, told late, from one said over the reply by that, and a peak just before the turn stopped
// listening is still the request.
func watchTalkingOver(e *Engine, told func(at time.Time)) {
	detected := e.OnDetect
	e.OnDetect = func(slot int) {
		if talkingOver(slot) {
			told(time.Now())
		}
		if detected != nil {
			detected(slot)
		}
	}
	candidate := e.OnCandidate
	e.OnCandidate = func(slot int, peak float64) {
		if talkingOver(slot) {
			told(time.Now())
		}
		if candidate != nil {
			candidate(slot, peak)
		}
	}
	near := e.OnNearMiss
	e.OnNearMiss = func(slot int, peak float64, at time.Time) {
		if near != nil {
			near(slot, peak, at)
		}
		if talkingOver(slot) {
			told(at)
		}
	}
}
