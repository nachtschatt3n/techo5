package mealie

import (
	"math"
	"strconv"
	"strings"
	"unicode"
)

// Amount is an ingredient's line at a scale (1 is the recipe as written). The second result says whether
// the line is one that can be scaled at all: a parsed one, with an amount and a food or a unit. A line
// like "a pinch of salt", or one with no amount, comes back as the server wrote it whatever the scale,
// and the screen then says some amounts were not scaled.
//
// At scale 1 the server's own line is used, since it is written in the server's language and with its
// units; at any other scale the line is put together again from the amount, the unit, the food and the
// note, with the unit and the food in the plural when the amount is more than one.
func Amount(ing Ingredient, scale float64) (text string, scaled bool) {
	asWritten := Normalize(ing.Display)
	if asWritten == "" {
		asWritten = strings.TrimSpace(ing.Note)
	}
	if !parsed(ing, asWritten) {
		return asWritten, false
	}
	if scale <= 0 || math.IsNaN(scale) || math.IsInf(scale, 0) {
		scale = 1
	}
	if scale == 1 && asWritten != "" {
		return asWritten, true
	}
	qty := ing.Quantity * scale
	if math.IsNaN(qty) || math.IsInf(qty, 0) {
		return asWritten, false
	}
	fraction := ing.Unit == nil || ing.Unit.Fraction
	parts := []string{formatQuantity(qty, fraction)}
	if u := ing.Unit; u != nil {
		if n := unitName(*u, qty > 1); n != "" {
			parts = append(parts, n)
		}
	}
	if f := ing.Food; f != nil {
		n := f.Name
		if ing.Unit == nil && qty > 1 && f.PluralName != "" {
			n = f.PluralName
		}
		if n != "" {
			parts = append(parts, n)
		}
	}
	if n := strings.TrimSpace(ing.Note); n != "" {
		parts = append(parts, n)
	}
	return strings.Join(parts, " "), true
}

// parsed is whether a line has an amount to scale: more than 0 of a food or a unit, and a line that
// begins with a number when the server wrote one ("a pinch of salt" has an amount of 1 and no number).
func parsed(ing Ingredient, line string) bool {
	if ing.Quantity <= 0 || (ing.Food == nil && ing.Unit == nil) {
		return false
	}
	if line == "" {
		return true
	}
	r := []rune(line)[0]
	return unicode.IsDigit(r) || strings.ContainsRune("½¼¾⅓⅔⅕⅖⅗⅘⅙⅚⅛⅜⅝⅞", r)
}

func unitName(u Unit, plural bool) string {
	name, many := u.Name, u.PluralName
	if u.UseAbbreviation && u.Abbreviation != "" {
		name, many = u.Abbreviation, u.PluralAbbreviation
	}
	if plural && many != "" {
		return many
	}
	return name
}

// formatQuantity writes an amount the way a cook reads it. With fraction, an amount under 10 near a
// half, third, quarter or eighth (within 1%) is written as one ("1 1/2"); any other amount under 10 gets
// one decimal, and from 10 up a whole number or one decimal. Without fraction it is up to two decimals,
// trailing zeros dropped. From 10000 up it is a whole number, never an exponent.
func formatQuantity(q float64, fraction bool) string {
	switch {
	case q >= 10000:
		return strconv.FormatFloat(math.Round(q), 'f', 0, 64)
	case q >= 10:
		return decimals(q, 1)
	case !fraction:
		return decimals(q, 2)
	}
	whole := math.Floor(q)
	for _, d := range []float64{2, 3, 4, 8} {
		for n := 0.0; n <= d; n++ {
			if math.Abs(q-(whole+n/d)) > 0.01*q {
				continue
			}
			w := int(whole)
			switch n {
			case 0:
				return strconv.Itoa(w)
			case d:
				return strconv.Itoa(w + 1)
			}
			f := strconv.Itoa(int(n)) + "/" + strconv.Itoa(int(d))
			if w == 0 {
				return f
			}
			return strconv.Itoa(w) + " " + f
		}
	}
	return decimals(q, 1)
}

// decimals writes q with at most n decimals and no trailing zeros. An amount so small it would read 0 is
// shown with two instead.
func decimals(q float64, n int) string {
	s := trimZeros(strconv.FormatFloat(q, 'f', n, 64))
	if s == "0" && q > 0 {
		s = trimZeros(strconv.FormatFloat(q, 'f', 2, 64))
		if s == "0" {
			return "0.01"
		}
	}
	return s
}

func trimZeros(s string) string {
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	return s
}

var plainFractions = map[rune]string{
	'½': "1/2", '¼': "1/4", '¾': "3/4", '⅓': "1/3", '⅔': "2/3", '⅕': "1/5", '⅖': "2/5", '⅗': "3/5",
	'⅘': "4/5", '⅙': "1/6", '⅚': "5/6", '⅛': "1/8", '⅜': "3/8", '⅝': "5/8", '⅞': "7/8", '⁄': "/",
}

// digitOf is the ASCII digit for a superscript or subscript one, and whether r is one.
func digitOf(r rune) (string, bool) {
	switch r {
	case '⁰', '₀':
		return "0", true
	case '¹', '₁':
		return "1", true
	case '²', '₂':
		return "2", true
	case '³', '₃':
		return "3", true
	}
	if r >= '⁴' && r <= '⁹' {
		return strconv.Itoa(int(r-'⁴') + 4), true
	}
	if r >= '₄' && r <= '₉' {
		return strconv.Itoa(int(r-'₄') + 4), true
	}
	return "", false
}

// Normalize makes a line from the server plain: Mealie writes a fraction with superscript and subscript
// digits ("1 ¹/₂"), which the screen's fonts may not have, and a glyph like "½" is the same. Both become
// ASCII ("1 1/2"), with a space kept between a whole number and its fraction. Runs of spaces collapse.
func Normalize(s string) string {
	var b strings.Builder
	var prev rune
	for _, r := range s {
		out, changed := string(r), false
		if v, ok := plainFractions[r]; ok {
			out, changed = v, true
		} else if v, ok := digitOf(r); ok {
			out, changed = v, true
		}
		// A glyph or a superscript right after a digit would run into it ("1" "1/2" is not "11/2").
		isSub := r >= '₀' && r <= '₉'
		if changed && r != '⁄' && !isSub && prev >= '0' && prev <= '9' {
			b.WriteByte(' ')
		}
		b.WriteString(out)
		prev = r
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
