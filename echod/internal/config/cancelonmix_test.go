package config

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// A device nobody has changed follows its board, whichever way the board goes, and its file says
// nothing about it: a settings file made from it then leaves the choice to the device it is restored on.
func TestCancelOnMixFollowsTheBoardUntilSet(t *testing.T) {
	got := load(t).Get().Microphone
	if got.CancelOnMix != nil {
		t.Fatalf("CancelOnMix = %v on a new device, want unset", *got.CancelOnMix)
	}
	for _, board := range []bool{false, true} {
		if v := got.CancelOnMixOr(board); v != board {
			t.Errorf("unset, board %v: %v, want the board's", board, v)
		}
	}
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "cancel_on_mix") {
		t.Errorf("an unset choice is written to the file: %s", b)
	}
}

// Set either way, it is kept over a restart and no longer follows the board, including set to what the
// board would have chosen anyway.
func TestCancelOnMixIsKept(t *testing.T) {
	for _, want := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "state.json")
		st, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := st.Set().Microphone().CancelOnMix(want); err != nil {
			t.Fatalf("saving %v: %v", want, err)
		}
		again, err := Load(path)
		if err != nil {
			t.Fatal(err)
		}
		m := again.Get().Microphone
		if m.CancelOnMix == nil {
			t.Fatalf("set to %v, came back unset", want)
		}
		for _, board := range []bool{false, true} {
			if v := m.CancelOnMixOr(board); v != want {
				t.Errorf("set to %v, board %v: %v", want, board, v)
			}
		}
	}
}

// What one Config hands out is not shared with a copy of it: the pointer is copied with the rest.
func TestCancelOnMixIsCopiedDeep(t *testing.T) {
	on := true
	c := Defaults()
	c.Microphone.CancelOnMix = &on
	d := deepCopy(c)
	*c.Microphone.CancelOnMix = false
	if d.Microphone.CancelOnMix == nil || !*d.Microphone.CancelOnMix {
		t.Error("changing the original changed the copy")
	}
}

// A settings file carries the choice to a device of the same model, which is the same board, and not to
// another: there the device keeps what it had, set or following its own board.
func TestCancelOnMixTravelsOnlyToTheSameModel(t *testing.T) {
	on, off := true, false
	from := Defaults()
	from.Microphone.CancelOnMix = &on
	file, err := Export(from, "Echo Dot 2nd gen (biscuit)", "v0")
	if err != nil {
		t.Fatal(err)
	}

	r, err := Restored(Defaults(), file, "Echo Dot 2nd gen (biscuit)")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Config.Microphone.CancelOnMix; got == nil || !*got {
		t.Errorf("same model: %v, want the file's on", got)
	}

	r, err = Restored(Defaults(), file, "Echo Show 5 2nd gen (cronos)")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Config.Microphone.CancelOnMix; got != nil {
		t.Errorf("another model, unset here: %v, want it left to this board", *got)
	}

	here := Defaults()
	here.Microphone.CancelOnMix = &off
	r, err = Restored(here, file, "Echo Show 5 2nd gen (cronos)")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.Config.Microphone.CancelOnMix; got == nil || *got {
		t.Errorf("another model, off here: %v, want this device's off", got)
	}
	if got := r.Config.Microphone.CancelOnMix; got == here.Microphone.CancelOnMix {
		t.Error("the restored setting shares this device's pointer")
	}
}
