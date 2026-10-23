// Package textclean reduces a scanned Message body to the short, plain text
// the engine and the Judge see. Standard library only.
package textclean

import (
	"html"
	"regexp"
	"strings"
	"unicode"
)

// Truncated marks a body that was cut short by the max rune cap.
const Truncated = " […]"

var (
	scriptRe  = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>`)
	styleRe   = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style\s*>`)
	commentRe = regexp.MustCompile(`(?s)<!--.*?-->`)
	// Tags that end a visual line become newlines so quoted replies and
	// signatures stay detectable.
	breakRe = regexp.MustCompile(`(?is)<br\s*/?>|</?(p|div|li|tr)\b[^>]*>`)
	tagRe   = regexp.MustCompile(`(?s)<[^>]*>`)

	horizontalWSRe = regexp.MustCompile(`[^\S\n]+`)
	blankLinesRe   = regexp.MustCompile(`\n{2,}`)

	// "On Tue, 5 Mar 2024 at 10:00, Alice <alice@example.com> wrote:"
	quotedReplyRe = regexp.MustCompile(`(?i)^on\b.*\bwrote:$`)
)

// Strip returns the cleaned body of a Message: the plain part when there is
// one, otherwise the HTML part with its markup removed. Invisible characters
// are dropped, whitespace is collapsed, quoted replies and signatures are cut
// off, and the result is capped at max runes with Truncated appended when it
// was cut. A max of zero or less means no cap.
func Strip(plain, htmlBody string, max int) string {
	text := plain
	if strings.TrimSpace(text) == "" {
		text = stripHTML(htmlBody)
	}
	text = removeInvisible(text)
	text = collapse(text)
	text = strings.TrimSpace(text)
	text = dropTails(text)
	text = strings.TrimSpace(text)
	return capRunes(text, max)
}

// stripHTML turns an HTML body into plain text: scripts and styles go with
// their content, line-ending tags become newlines, every other tag is dropped
// and entities are unescaped.
func stripHTML(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	// Script and style blocks become a newline, not nothing, so removing one
	// never glues two words together. A comment is invisible mid-sentence, so
	// it goes without a trace, as a browser would render it.
	s = scriptRe.ReplaceAllString(s, "\n")
	s = styleRe.ReplaceAllString(s, "\n")
	s = commentRe.ReplaceAllString(s, "")
	s = breakRe.ReplaceAllString(s, "\n")
	s = tagRe.ReplaceAllString(s, "")
	return html.UnescapeString(s)
}

// removeInvisible drops zero-width and bidi control characters, which senders
// use to break up keywords.
func removeInvisible(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 0x200B && r <= 0x200F, // zero width space .. RLM
			r >= 0x202A && r <= 0x202E, // bidi embedding/override
			r >= 0x2060 && r <= 0x2064, // word joiner .. invisible plus
			r == 0xFEFF:                // byte order mark
			return -1
		}
		return r
	}, s)
}

// collapse normalises line endings, squeezes runs of horizontal whitespace
// (non-breaking spaces included) to one space and runs of blank lines to one
// newline.
func collapse(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	s = strings.Map(func(r rune) rune {
		if r != '\n' && unicode.IsSpace(r) {
			return ' '
		}
		return r
	}, s)
	s = horizontalWSRe.ReplaceAllString(s, " ")
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSpace(line)
	}
	s = strings.Join(lines, "\n")
	return blankLinesRe.ReplaceAllString(s, "\n")
}

// dropTails cuts the body at the first quoted reply or signature separator.
func dropTails(s string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, ">") ||
			trimmed == "--" ||
			quotedReplyRe.MatchString(trimmed) {
			return strings.Join(lines[:i], "\n")
		}
	}
	return s
}

// capRunes limits the text to max runes, marking it when it was cut.
func capRunes(s string, max int) string {
	if max <= 0 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return strings.TrimRight(string(runes[:max]), " \n\t") + Truncated
}
