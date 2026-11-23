package mealie

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode"
)

// Found is a time written in a step, with the words it was written in and how long it is.
type Found struct {
	Text string
	D    time.Duration
}

// A recipe is often in another language than the screen, so English and German are always both tried.
// The pieces below make one pattern; a time is a number, maybe a range, and a unit, or one of a few
// phrases ("half an hour", "eine Viertelstunde").
const (
	space = `[\s\x{a0}\x{202f}]`

	// Digits first ("1:30", "1 1/2", "1/2", "1½", "1,5", "20"), then the words. A longer word comes
	// before a shorter one it starts with, since the first that fits wins.
	number = `(?:\d+:\d{2}|\d+` + space + `+\d+/\d+|\d+/\d+|\d+` + space + `?[½¼¾⅓⅔]|\d+(?:[.,]\d+)?|[½¼¾⅓⅔]|` +
		`forty` + space + `?-?` + space + `?five|fünfundvierzig|fifteen|fünfzehn|thirty|dreißig|dreissig|twenty|zwanzig|` +
		`eleven|twelve|zwölf|seven|sieben|eight|acht|three|drei|four|vier|five|fünf|nine|neun|` +
		`two|zwei|six|sechs|ten|zehn|elf|one|einer|eine|ein|an|a)`

	unit = `(?:seconds?|secs?|sekunden?|sek\.?|minutes?|minuten?|mins?\.?|hours?|hrs?\.?|stunden?|std\.?|h)`

	prefix = `(?:(?:ca\.?|circa|about|approx\.?|approximately|around|etwa|ungefähr|rund)` + space + `+)?`

	phrase = `half` + space + `+an` + space + `+hour|half` + space + `+hour|` +
		`(?:eine` + space + `+)?halbe` + space + `+stunde|` +
		`(?:anderthalb|eineinhalb)` + space + `+stunden?|` +
		`drei` + space + `+viertelstunden?|dreiviertelstunde|(?:eine` + space + `+)?viertelstunde`
)

// The pattern takes the longest match, not the first that fits, so "Minuten" is not cut to "Minute" with an
// "n" left over for the end check to refuse.
var stepTime = func() *regexp.Regexp {
	re := regexp.MustCompile(`(?i)(?P<all>(?P<phrase>` + phrase + `)|` + prefix +
		`(?P<n1>` + number + `)(?:` + space + `*(?:-|–|—|to|bis)` + space + `*(?P<n2>` + number + `))?` +
		space + `*(?P<unit>` + unit + `))`)
	re.Longest()
	return re
}()

var (
	iAll    = stepTime.SubexpIndex("all")
	iPhrase = stepTime.SubexpIndex("phrase")
	iN1     = stepTime.SubexpIndex("n1")
	iUnit   = stepTime.SubexpIndex("unit")
)

// Durations are the times written in a step's text, at most three, in the order they appear. A range
// ("10-15 Min.", "10 bis 15 Minuten") gives its lower bound, the text keeps the whole range. Anything over a
// day is ignored, and so is a letter or digit running into either end ("Minutenlang", "Stundenplan"),
// a bare "m" or "s", and anything that is not a time ("180 °C", "2 EL", "Step 1.").
func Durations(text string) []Found {
	var out []Found
	for _, m := range stepTime.FindAllStringSubmatchIndex(text, -1) {
		start, end := m[2*iAll], m[2*iAll+1]
		if start > 0 && wordRune(lastRune(text[:start])) || end < len(text) && wordRune(firstRune(text[end:])) {
			continue
		}
		var d time.Duration
		if m[2*iPhrase] >= 0 {
			d = phraseDuration(strings.ToLower(text[m[2*iPhrase]:m[2*iPhrase+1]]))
		} else {
			d = numberDuration(strings.ToLower(text[m[2*iN1]:m[2*iN1+1]]), strings.ToLower(text[m[2*iUnit]:m[2*iUnit+1]]))
		}
		if d <= 0 || d > 24*time.Hour {
			continue
		}
		out = append(out, Found{Text: strings.TrimSpace(text[start:end]), D: d})
		if len(out) == 3 {
			break
		}
	}
	return out
}

func wordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

func lastRune(s string) rune {
	r := []rune(s)
	return r[len(r)-1]
}

func firstRune(s string) rune { return []rune(s)[0] }

func phraseDuration(p string) time.Duration {
	switch {
	case strings.HasPrefix(p, "half"), strings.Contains(p, "halbe"):
		return 30 * time.Minute
	case strings.Contains(p, "anderthalb"), strings.Contains(p, "eineinhalb"):
		return 90 * time.Minute
	case strings.Contains(p, "drei"):
		return 45 * time.Minute
	}
	return 15 * time.Minute
}

func numberDuration(n, u string) time.Duration {
	per := time.Hour
	switch {
	case strings.HasPrefix(u, "sek"), strings.HasPrefix(u, "sec"):
		per = time.Second
	case strings.HasPrefix(u, "min"):
		per = time.Minute
	}
	if h, m, ok := strings.Cut(n, ":"); ok {
		// 1:30 is hours and minutes before an hour unit, minutes and seconds before a minute unit, and
		// nothing before seconds.
		a, _ := strconv.Atoi(h)
		b, _ := strconv.Atoi(m)
		switch per {
		case time.Hour:
			return time.Duration(a)*time.Hour + time.Duration(b)*time.Minute
		case time.Minute:
			return time.Duration(a)*time.Minute + time.Duration(b)*time.Second
		}
		return 0
	}
	v, ok := numberValue(n)
	if !ok {
		return 0
	}
	return time.Duration(math.Round(v * float64(per)))
}

var wordNumbers = map[string]float64{
	"a": 1, "an": 1, "one": 1, "two": 2, "three": 3, "four": 4, "five": 5, "six": 6, "seven": 7, "eight": 8,
	"nine": 9, "ten": 10, "eleven": 11, "twelve": 12, "fifteen": 15, "twenty": 20, "thirty": 30,
	"fortyfive": 45, "forty-five": 45,
	"ein": 1, "eine": 1, "einer": 1, "zwei": 2, "drei": 3, "vier": 4, "fünf": 5, "sechs": 6, "sieben": 7,
	"acht": 8, "neun": 9, "zehn": 10, "elf": 11, "zwölf": 12, "fünfzehn": 15, "zwanzig": 20,
	"dreißig": 30, "dreissig": 30, "fünfundvierzig": 45,
}

var glyphValues = map[rune]float64{'½': .5, '¼': .25, '¾': .75, '⅓': 1.0 / 3, '⅔': 2.0 / 3}

// numberValue reads what the number pattern matched: digits with a decimal comma or point, a mixed
// number ("1 1/2"), a glyph ("1½"), or a word.
func numberValue(n string) (float64, bool) {
	n = strings.TrimSpace(n)
	if v, ok := wordNumbers[strings.Join(strings.Fields(n), "")]; ok {
		return v, true
	}
	if v, ok := wordNumbers[n]; ok {
		return v, true
	}
	total := 0.0
	for _, f := range strings.Fields(strings.NewReplacer("½", " ½", "¼", " ¼", "¾", " ¾", "⅓", " ⅓", "⅔", " ⅔").Replace(n)) {
		switch {
		case strings.Contains(f, "/"):
			a, b, _ := strings.Cut(f, "/")
			x, err1 := strconv.ParseFloat(a, 64)
			y, err2 := strconv.ParseFloat(b, 64)
			if err1 != nil || err2 != nil || y == 0 {
				return 0, false
			}
			total += x / y
		case len([]rune(f)) == 1 && glyphValues[[]rune(f)[0]] != 0:
			total += glyphValues[[]rune(f)[0]]
		default:
			x, err := strconv.ParseFloat(strings.Replace(f, ",", ".", 1), 64)
			if err != nil {
				return 0, false
			}
			total += x
		}
	}
	return total, total > 0
}
