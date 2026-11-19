package voice

import (
	"testing"
	"time"
)

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
