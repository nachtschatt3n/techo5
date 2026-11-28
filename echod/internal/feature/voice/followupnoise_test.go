package voice

import (
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
)

// answered takes the open turn from listening, after listened, through a reply to the end of it, with
// Home Assistant asking a question back when asks is set. Whatever follows the reply has opened, or not,
// when it returns.
//
// The turn's own run starts first, as Home Assistant's does: a follow-up opens right after a reply, and
// until its RUN_START comes what arrives is taken for the run before's and dropped (#776).
func answered(t *testing.T, w *watcher, listened time.Duration, asks bool) {
	t.Helper()
	if w.phase != phaseListening {
		t.Fatalf("no turn listening: %s", w.phase)
	}
	w.handle(event{kind: evRunStart})
	w.at = w.at.Add(listened)
	w.handle(event{kind: evHeard, text: "The kitchen."})
	if asks {
		w.handle(event{kind: evContinue})
	}
	w.handle(event{kind: evReplyText, text: "Hm?"})
	w.enter(phaseReplying)
	w.handle(event{kind: evPlayed})
	w.flush()
}

// woken opens a turn on the wake word.
func woken(t *testing.T, w *watcher) {
	t.Helper()
	w.handle(event{kind: evStart, slot: 0, woke: true, at: w.at})
	if w.phase != phaseListening || w.followUp {
		t.Fatalf("the wake word opened no turn: %s, follow-up %v", w.phase, w.followUp)
	}
}

// A television woke the device, the assistant asked what was meant, and the follow-up heard the
// television to the limit. The assistant's next question is said and not listened for, so the device
// does not go on asking the television what it meant.
func TestAQuestionAfterAFollowUpHeldOpenIsNotListenedFor(t *testing.T) {
	w := newWatcher(t, config.OnHA{})
	woken(t, w)
	answered(t, w, 3*time.Second, true)
	if w.phase != phaseListening || !w.followUp {
		t.Fatalf("the first question was not listened for: %s, follow-up %v", w.phase, w.followUp)
	}
	answered(t, w, 15*time.Second, true)
	if w.phase != phaseIdle || w.pending != nil {
		t.Errorf("a question after a follow-up held open to the limit was listened for: %s, pending %+v", w.phase, w.pending)
	}
}

// Somebody answering a question finishes, Home Assistant hears the end well before the limit, and a
// question it asks back is listened for as before.
func TestAnAnsweredQuestionStillOpensTheNext(t *testing.T) {
	w := newWatcher(t, config.OnHA{})
	woken(t, w)
	answered(t, w, 3*time.Second, true)
	answered(t, w, 2500*time.Millisecond, true)
	if w.phase != phaseListening || !w.followUp {
		t.Errorf("the second question of a real dialogue was not listened for: %s, follow-up %v", w.phase, w.followUp)
	}
	if w.questions != 2 {
		t.Errorf("questions %d, want 2", w.questions)
	}
}

// A long request after the wake word is still somebody who spoke to the device: its question is
// listened for whatever the request's length.
func TestAWakeWordTurnHeldOpenStillGetsItsQuestion(t *testing.T) {
	w := newWatcher(t, config.OnHA{})
	woken(t, w)
	answered(t, w, 15*time.Second, true)
	if w.phase != phaseListening || !w.followUp {
		t.Errorf("the question after a long request was not listened for: %s", w.phase)
	}
}

// A sound with pauses in it lets Home Assistant end every listen early, so none is held open: the
// questions in a row stop at questionsInRow, and the wake word starts the count again.
func TestQuestionsInARowStopAtTheLimit(t *testing.T) {
	w := newWatcher(t, config.OnHA{})
	woken(t, w)
	for i := range questionsInRow {
		answered(t, w, 4*time.Second, true)
		if w.phase != phaseListening || w.questions != i+1 {
			t.Fatalf("question %d was not listened for: %s, questions %d", i+1, w.phase, w.questions)
		}
	}
	answered(t, w, 4*time.Second, true)
	if w.phase != phaseIdle || w.pending != nil {
		t.Errorf("question %d in a row was listened for: %s", questionsInRow+1, w.phase)
	}

	woken(t, w)
	if w.questions != 0 {
		t.Errorf("the wake word left questions at %d", w.questions)
	}
	answered(t, w, 4*time.Second, true)
	if w.phase != phaseListening {
		t.Errorf("the first question after the wake word was not listened for: %s", w.phase)
	}
}

// Listening after every reply stops the same way: a follow-up held open to the limit is not followed by
// another.
func TestAFollowUpHeldOpenEndsListeningAfterEveryReply(t *testing.T) {
	w := newWatcher(t, config.OnHA{})
	if err := config.Set().Wake(0).FollowUp(5); err != nil {
		t.Fatal(err)
	}
	woken(t, w)
	answered(t, w, 3*time.Second, false)
	if w.phase != phaseListening || !w.followUp {
		t.Fatalf("no follow-up after the reply: %s", w.phase)
	}
	answered(t, w, 2*time.Second, false)
	if w.phase != phaseListening {
		t.Fatalf("a short follow-up was not followed: %s", w.phase)
	}
	answered(t, w, 15*time.Second, false)
	if w.phase != phaseIdle {
		t.Errorf("a follow-up held open to the limit was followed by another: %s", w.phase)
	}
}
