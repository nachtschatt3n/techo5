package mealie

import (
	"regexp"
	"strings"
)

var (
	mdImage   = regexp.MustCompile(`!\[([^\]]*)\]\([^)]*\)`)
	mdLink    = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	mdBold    = regexp.MustCompile(`(\*\*|__)(.+?)(\*\*|__)`)
	mdStar    = regexp.MustCompile(`\*([^*\n]+)\*`)
	mdUnder   = regexp.MustCompile(`(^|[^\pL\pN_])_([^_\n]+)_($|[^\pL\pN_])`)
	mdStrike  = regexp.MustCompile(`~~(.+?)~~`)
	mdCode    = regexp.MustCompile("`([^`\n]*)`")
	mdHead    = regexp.MustCompile(`(?m)^[ \t]*#{1,6}[ \t]+`)
	mdList    = regexp.MustCompile(`(?m)^[ \t]*(?:[-*+]|\d+[.)])[ \t]+`)
	mdTag     = regexp.MustCompile(`</?[a-zA-Z][^>]*>`)
	manyLines = regexp.MustCompile(`\n{3,}`)
)

// Plain is a step's text without its markdown: links keep their words, bold and italics lose their
// marks, and a heading or a list loses its marker. Line breaks stay, since a step often has a few.
func Plain(text string) string {
	t := strings.ReplaceAll(text, "\r\n", "\n")
	t = mdImage.ReplaceAllString(t, "$1")
	t = mdLink.ReplaceAllString(t, "$1")
	t = mdBold.ReplaceAllString(t, "$2")
	t = mdStar.ReplaceAllString(t, "$1")
	t = mdUnder.ReplaceAllString(t, "$1$2$3")
	t = mdStrike.ReplaceAllString(t, "$1")
	t = mdCode.ReplaceAllString(t, "$1")
	t = mdHead.ReplaceAllString(t, "")
	t = mdList.ReplaceAllString(t, "")
	t = mdTag.ReplaceAllString(t, "")
	lines := strings.Split(t, "\n")
	for i, l := range lines {
		lines[i] = strings.Join(strings.Fields(l), " ")
	}
	return strings.TrimSpace(manyLines.ReplaceAllString(strings.Join(lines, "\n"), "\n\n"))
}
