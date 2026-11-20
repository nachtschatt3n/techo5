package voice

import (
	"testing"
	"time"

	"github.com/HuskerMinion/techo5/echod/internal/config"
)

// canceller stands in for the echo canceller for the rest of the test, and reports whether it was
// last told to learn.
func canceller(t *testing.T) *bool {
	t.Helper()
	was := setAdapting
	t.Cleanup(func() { setAdapting = was })
	learns := true
	setAdapting = func(on bool) { learns = on }
	return &learns
}

// heldTurn is a thinking turn holding the speaker, as one opened by a wake word is, with the
// canceller stood in for and the microphones free unless busy says otherwise.
func heldTurn(t *testing.T, busy bool) (*conversation, *bool) {
	t.Helper()
	c := thinkingTurn(t)
	learns := canceller(t)
	c.busy = func() bool { return busy }
	c.hold(true) // as start does
	if *learns {
		t.Fatal("the canceller learns while the turn listens or thinks")
	}
	return c, learns
}

// replyStarts gives the turn a reply and waits for its first audio.
func replyStarts(t *testing.T, c *conversation, learns *bool) {
	t.Helper()
	url, release := heldSpeech(t)
	c.handle(event{kind: evReplyText, text: "It's noon."})
	c.handle(event{kind: evReplyURL, url: url})
	if *learns {
		t.Fatal("the canceller learns before the reply has any audio")
	}
	close(release)
	c.handle(next(t, c, evPlaying))
}

// The built-in canceller learns while the reply plays, and nobody is being listened to: the turn
// holding the speaker no longer keeps it frozen from the wake word to the end. Once the reply has been
// heard and no turn follows, it learns as it always has outside a turn.
func TestTheCancellerLearnsWhileTheReplyPlays(t *testing.T) {
	c, learns := heldTurn(t, false)
	// No follow-up after the reply, whatever the default: the turn ends with it.
	if err := config.Set().Wake(0).FollowUp(0); err != nil {
		t.Fatal(err)
	}
	replyStarts(t, c, learns)
	if !*learns {
		t.Fatal("the canceller is frozen while only the reply plays")
	}

	c.handle(next(t, c, evPlayed))
	if c.phase != phaseIdle || c.learning {
		t.Fatalf("after the reply the turn is %s, learning over it %v", c.phase, c.learning)
	}
	if !*learns {
		t.Error("with no turn open the canceller is still frozen")
	}
}

// A wake word or the stop word heard, or nearly heard, over the reply is somebody talking over it: the
// canceller stops learning then, and a second try is not learned from either.
func TestAWordOverTheReplyFreezesTheCanceller(t *testing.T) {
	c, learns := heldTurn(t, false)
	replyStarts(t, c, learns)

	c.handle(event{kind: evTalkedOver})
	if *learns {
		t.Fatal("the canceller goes on learning with somebody talking over the reply")
	}
	c.handle(event{kind: evPlaying, reply: c.reply.id})
	if *learns {
		t.Error("the reply's audio started the learning again after a word over it")
	}
}

// A word said while the turn waits on its answer is somebody talking too: the reply that follows is not
// learned over.
func TestAWordWhileThinkingKeepsTheReplyFrozen(t *testing.T) {
	c, learns := heldTurn(t, false)
	c.handle(event{kind: evTalkedOver})
	replyStarts(t, c, learns)
	if *learns {
		t.Error("the canceller learns over a reply somebody was talking before")
	}
}

// While the turn listens, a near miss is the request itself: it says nothing about the reply.
func TestAWordWhileListeningDoesNotCount(t *testing.T) {
	c, _ := heldTurn(t, false)
	c.enter(phaseListening)
	c.handle(event{kind: evTalkedOver})
	if c.talkedOver {
		t.Error("a word in the request was taken for one over the reply")
	}
}

// A wake word over the reply interrupts it, and the turn it opens is held back until the old run has
// closed: the canceller is frozen from the interruption on, through the wait, not learning from whoever
// is now talking.
func TestAWakeWordOverTheReplyFreezesTheCancellerForTheNextTurn(t *testing.T) {
	c, learns := heldTurn(t, false)
	replyStarts(t, c, learns)

	if c.phase != phaseReplying || !*learns {
		t.Fatalf("before the wake the turn is %s, learning %v", c.phase, *learns)
	}

	// Replying, not listening, and the microphones live (no mute pin on a test machine): start takes
	// the interruption.
	c.handle(event{kind: evStart, slot: 0, woke: true, at: time.Now()})
	if c.pending == nil {
		t.Fatalf("the wake did not interrupt the reply: phase %s", c.phase)
	}
	if *learns {
		t.Error("the canceller learns while the next turn waits to open")
	}
}

// A call up, or the microphones taken or muted: the reply is not learned over.
func TestNoLearningOverTheReplyWithTheMicrophonesBusy(t *testing.T) {
	c, learns := heldTurn(t, true)
	replyStarts(t, c, learns)
	if *learns {
		t.Error("the canceller learns over a reply during a call")
	}
}

// TalkedOver reaches the turn only once it has stopped listening; at other times nothing would act on
// it, and a room full of near misses would fill the queue.
func TestTalkedOverIsPostedOnlyOverAnAnswer(t *testing.T) {
	idle := idleTurn(t)
	(&Voice{turn: idle}).TalkedOver(time.Now())
	select {
	case e := <-idle.events:
		t.Errorf("with no turn open TalkedOver posted %v", e.kind)
	default:
	}

	c := thinkingTurn(t)
	(&Voice{turn: c}).TalkedOver(time.Now())
	select {
	case e := <-c.events:
		if e.kind != evTalkedOver {
			t.Fatalf("TalkedOver posted %v", e.kind)
		}
	default:
		t.Fatal("TalkedOver while thinking posted nothing")
	}
}

// A near miss is told a little more than NearMissSettle after its peak, with when it peaked, so one that
// peaked in the request can arrive once the turn is thinking. Said before the turn stopped listening,
// even just before, it is the request, not a word over the reply; one said after is.
func TestAWordSaidInTheRequestAndToldLateDoesNotCount(t *testing.T) {
	c, _ := heldTurn(t, false)
	c.enter(phaseListening)
	c.think()

	c.handle(event{kind: evTalkedOver, at: c.listenedTo.Add(-10 * time.Millisecond)})
	if c.talkedOver {
		t.Fatal("a near miss said just before the turn stopped listening was taken for one over the reply")
	}
	c.handle(event{kind: evTalkedOver, at: time.Now()})
	if !c.talkedOver {
		t.Error("a word said after the request did not count")
	}
}

// A reply that takes over from another, as the url does from the streamed copy, learns from its own
// audio: from the moment it is asked for until it plays, the canceller is frozen again.
func TestANewReplyLearnsOnlyFromItsOwnAudio(t *testing.T) {
	c, learns := heldTurn(t, false)
	replyStarts(t, c, learns)
	if !*learns {
		t.Fatal("the canceller is frozen while the first reply plays")
	}

	second, release := heldSpeech(t)
	c.speak(second)
	if *learns {
		t.Fatal("the canceller learns before the second reply has any audio")
	}
	close(release)
	e := next(t, c, evPlaying)
	if e.reply != c.reply.id {
		t.Fatalf("audio of reply %d arrived, want the second, %d", e.reply, c.reply.id)
	}
	c.handle(e)
	if !*learns {
		t.Error("the canceller is frozen while the second reply plays")
	}
}

// The microphones muted, taken or on a call while the reply plays end the learning over it, which the
// reply's first audio could not know about.
func TestTheMicrophonesBusyDuringTheReplyStopTheLearning(t *testing.T) {
	c, learns := heldTurn(t, false)
	replyStarts(t, c, learns)
	if !*learns {
		t.Fatal("the canceller is frozen while the reply plays")
	}

	c.handle(event{kind: evWatch})
	if !*learns {
		t.Fatal("a check with the microphones free stopped the learning")
	}
	busy := true
	c.busy = func() bool { return busy }
	c.handle(event{kind: evWatch}) // as the mute switch posts on every change
	if *learns {
		t.Error("the canceller goes on learning with the microphones muted")
	}
}
