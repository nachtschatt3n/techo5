package voice

import (
	"testing"
	"time"
)

// A tap on the words of a reply being said ends the reply, as the stop word over it does.
func TestStopReplyEndsAReplyBeingSaid(t *testing.T) {
	c := thinkingTurn(t)
	reply, release := heldSpeech(t)
	close(release)
	c.handle(event{kind: evReplyText, text: "It's noon."})
	c.handle(event{kind: evReplyURL, url: reply})
	c.handle(next(t, c, evPlaying))
	if c.phase != phaseReplying {
		t.Fatalf("the turn is %s, want replying", c.phase)
	}
	v := &Voice{turn: c}

	if !v.StopReply() {
		t.Fatal("StopReply found no reply being said")
	}
	c.handle(next(t, c, evCancel))
	if c.Busy() || c.sound.Busy() {
		t.Fatalf("the reply is still going: turn %s, sound busy %v", c.phase, c.sound.Busy())
	}
}

// The stop word cancels a turn that is still thinking, but a tap on its words does not: only a
// reply being said is stopped.
func TestStopReplyLeavesATurnNotReplyingAlone(t *testing.T) {
	for name, c := range map[string]*conversation{"thinking": thinkingTurn(t), "idle": idleTurn(t)} {
		v := &Voice{turn: c}
		if v.StopReply() {
			t.Errorf("%s: StopReply reported a reply stopped", name)
		}
		select {
		case e := <-c.events:
			t.Errorf("%s: a tap posted %v", name, e.kind)
		default:
		}
	}
}

// Cancel request ends a request that is running, and does nothing with none running.
func TestCancelRequestEndsARunningRequest(t *testing.T) {
	c := thinkingTurn(t)
	v := &Voice{turn: c}

	v.CancelRequest()
	select {
	case e := <-c.events:
		if e.kind != evCancel {
			t.Fatalf("the press posted %v, want a cancel", e.kind)
		}
		c.handle(e)
	case <-time.After(time.Second):
		t.Fatal("the press during a request did nothing")
	}
	if c.Busy() {
		t.Fatalf("the request is still %s", c.phase)
	}

	v.CancelRequest()
	select {
	case e := <-c.events:
		t.Errorf("a press with no request running posted %v", e.kind)
	default:
	}
}

// The stop word while the turn waits on its answer calls the answer off, as it does over the reply; it
// still does nothing while somebody is talking.
func TestTheStopWordWhileWaitingOnAnAnswerCancelsTheTurn(t *testing.T) {
	c := thinkingTurn(t)
	v := &Voice{turn: c}

	v.Interrupt()
	select {
	case e := <-c.events:
		if e.kind != evCancel {
			t.Fatalf("the stop word posted %v, want a cancel", e.kind)
		}
		c.handle(e)
	case <-time.After(time.Second):
		t.Fatal("the stop word while waiting on an answer did nothing")
	}
	if c.Busy() {
		t.Fatalf("the request is still %s", c.phase)
	}
}
