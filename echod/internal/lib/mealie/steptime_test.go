package mealie

import (
	"testing"
	"time"
)

func mins(n float64) time.Duration { return time.Duration(n * float64(time.Minute)) }

func TestDurations(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
		text string
	}{
		{"zehn Minuten kochen", mins(10), "zehn Minuten"},
		{"eine Stunde ruhen", 60 * time.Minute, "eine Stunde"},
		{"zwei Stunden", 120 * time.Minute, "zwei Stunden"},
		{"eine halbe Stunde", mins(30), "eine halbe Stunde"},
		{"anderthalb Stunden", mins(90), "anderthalb Stunden"},
		{"eineinhalb Stunden", mins(90), "eineinhalb Stunden"},
		{"eine Viertelstunde", mins(15), "eine Viertelstunde"},
		{"drei Viertelstunden", mins(45), "drei Viertelstunden"},
		{"eine Dreiviertelstunde", mins(45), "Dreiviertelstunde"},
		{"10 Min. köcheln", mins(10), "10 Min."},
		{"ca. 20 Min ziehen", mins(20), "ca. 20 Min"},
		{"1 Std. backen", 60 * time.Minute, "1 Std."},
		{"1,5 Std backen", mins(90), "1,5 Std"},
		{"1,5 Stunden", mins(90), "1,5 Stunden"},
		{"2 h", 120 * time.Minute, "2 h"},
		{"30 Sek. braten", 30 * time.Second, "30 Sek."},
		{"nach 2 Min. abgießen", mins(2), "2 Min."},
		{"about 10 minutes", mins(10), "about 10 minutes"},
		{"half an hour", mins(30), "half an hour"},
		{"1 1/2 hours", mins(90), "1 1/2 hours"},
		{"1½ hours", mins(90), "1½ hours"},
		{"1:30 h", mins(90), "1:30 h"},
		{"1:30 hours", mins(90), "1:30 hours"},
		{"a minute", mins(1), "a minute"},
		{"an hour", mins(60), "an hour"},
		{"fifteen minutes", mins(15), "fifteen minutes"},
		{"forty-five minutes", mins(45), "forty-five minutes"},
		{"fünfundvierzig Minuten", mins(45), "fünfundvierzig Minuten"},
		{"dreißig Sekunden", 30 * time.Second, "dreißig Sekunden"},
		{"Zwölf Minuten", mins(12), "Zwölf Minuten"},
		{"3 mins", mins(3), "3 mins"},
		{"20 seconds", 20 * time.Second, "20 seconds"},
		{"5min", mins(5), "5min"},
		{"bake 25 min, then rest", mins(25), "25 min"},
	} {
		got := Durations(tc.in)
		if len(got) != 1 || got[0].D != tc.want || got[0].Text != tc.text {
			t.Errorf("Durations(%q) = %+v, want %v %q", tc.in, got, tc.want, tc.text)
		}
	}
}

func TestDurationsDoNotMatch(t *testing.T) {
	for _, in := range []string{
		"Minutenlang rühren", "Stundenplan", "bei 180 Grad", "180 °C", "2 EL", "Schritt 1.", "Step 1.", "2 m",
		"per 100 g", "30 hours", "25 Stunden", "3 s", "5 minutely", "2 Minutenlang", "die Stunde null",
		"mit 10 Stundenkilometern", "1:30 Uhr", "12:30", "etwa", "Minuten", "ein Minutenzeiger",
	} {
		if got := Durations(in); len(got) != 0 {
			t.Errorf("Durations(%q) = %+v, want none", in, got)
		}
	}
}

func TestDurationsRangeLowerBound(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want time.Duration
		text string
	}{
		{"10-15 Min. kochen", mins(10), "10-15 Min."},
		{"10 bis 15 Minuten", mins(10), "10 bis 15 Minuten"},
		{"10–15 Minuten", mins(10), "10–15 Minuten"},
		{"20 to 25 minutes", mins(20), "20 to 25 minutes"},
		{"ca. 1 bis 2 Stunden", 60 * time.Minute, "ca. 1 bis 2 Stunden"},
	} {
		got := Durations(tc.in)
		if len(got) != 1 || got[0].D != tc.want || got[0].Text != tc.text {
			t.Errorf("Durations(%q) = %+v, want %v %q", tc.in, got, tc.want, tc.text)
		}
	}
}

func TestDurationsSeveralAndAtMostThree(t *testing.T) {
	got := Durations("Simmer 10 min, rest 5 min, then 20 Min. in the oven, then 1 h more.")
	if len(got) != 3 || got[0].D != mins(10) || got[1].D != mins(5) || got[2].D != mins(20) {
		t.Errorf("got %+v", got)
	}
	got = Durations("zwei Stunden, danach 30 Sek.")
	if len(got) != 2 || got[0].D != 120*time.Minute || got[1].D != 30*time.Second {
		t.Errorf("got %+v", got)
	}
}
