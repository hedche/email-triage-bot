// Package command turns Email Commands — messages an Account owner sends to
// their own plus-address — into Changes to the Overlay, with a Telegram
// confirmation for anything that could cause mail to be marked read.
package command

import (
	"net/mail"
	"regexp"
	"strings"
)

// Forwarded is the part of a forwarded message the Interpreter may see: who
// sent it and its subject. The forwarded body is third-party text and is never
// passed on.
type Forwarded struct {
	Name    string
	Address string // lower-cased
	Subject string
}

// Domain returns the part of Address after '@'.
func (f *Forwarded) Domain() string {
	if f == nil {
		return ""
	}
	if i := strings.LastIndexByte(f.Address, '@'); i >= 0 {
		return f.Address[i+1:]
	}
	return ""
}

// maxInstruction caps the owner's instruction. A rule change is a sentence or
// two; anything longer is almost certainly a quoted body that was not detected.
const maxInstruction = 1000

// forwardMarkers start the forwarded block in the clients people actually use.
var forwardMarkers = []string{
	"---------- forwarded message", // Gmail
	"-------- forwarded message",   // Thunderbird
	"begin forwarded message",      // Apple Mail
	"-----original message-----",   // Outlook
}

var (
	wroteLine  = regexp.MustCompile(`(?i)^on .+ wrote:$`)
	angleAddr  = regexp.MustCompile(`<([^<>\s]+@[^<>\s]+)>`)
	bareAddr   = regexp.MustCompile(`[^\s<>"'*]+@[^\s<>"'*]+\.[a-z]{2,}`)
	fwdPrefix  = regexp.MustCompile(`(?i)^\s*((fwd?|fw|re)\s*:\s*)+`)
	headerLine = regexp.MustCompile(`(?i)^\*?(from|subject|date|sent|to|cc)\*?\s*:`)
)

// Split separates the owner's instruction from a forwarded message in an Email
// Command body. subject is the command's own subject: it is part of the
// instruction only when nothing was forwarded, because a forward's subject is
// "Fwd: <the third party's subject>".
func Split(subject, body string) (instruction string, fwd *Forwarded) {
	lines := strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")

	var own []string
	rest := -1
	for i, line := range lines {
		t := strings.TrimSpace(line)
		lower := strings.ToLower(t)
		if isMarker(lower) || wroteLine.MatchString(t) || strings.HasPrefix(t, ">") {
			rest = i
			break
		}
		// Outlook and some mobile clients forward with no separator, just a
		// header block. A From: line followed closely by a Subject: line is it.
		if strings.HasPrefix(lower, "from:") && headerBlockFollows(lines[i:]) {
			rest = i
			break
		}
		if t == "--" || t == "-- " {
			break // signature
		}
		own = append(own, line)
	}

	if rest >= 0 {
		fwd = parseForwardedHeaders(lines[rest:])
	}

	instruction = strings.TrimSpace(strings.Join(own, "\n"))
	if fwd == nil {
		if s := strings.TrimSpace(fwdPrefix.ReplaceAllString(subject, "")); s != "" {
			instruction = strings.TrimSpace(s + "\n" + instruction)
		}
	}
	if r := []rune(instruction); len(r) > maxInstruction {
		instruction = string(r[:maxInstruction])
	}
	return instruction, fwd
}

func isMarker(lower string) bool {
	for _, m := range forwardMarkers {
		if strings.HasPrefix(lower, m) {
			return true
		}
	}
	return false
}

func headerBlockFollows(lines []string) bool {
	for i := 1; i < len(lines) && i < 6; i++ {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(lines[i])), "subject:") {
			return true
		}
	}
	return false
}

// parseForwardedHeaders reads From and Subject from the first header block in
// lines. It returns nil when no From address can be found.
func parseForwardedHeaders(lines []string) *Forwarded {
	f := &Forwarded{}
	inBlock := false
	for i, line := range lines {
		if i > 25 {
			break
		}
		t := strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), ">"))
		if !headerLine.MatchString(t) {
			if inBlock && t == "" {
				break // the blank line after the header block
			}
			continue
		}
		inBlock = true
		key, value, _ := strings.Cut(t, ":")
		key = strings.ToLower(strings.Trim(key, "* "))
		value = strings.TrimSpace(strings.Trim(strings.TrimSpace(value), "*"))
		switch key {
		case "from":
			if f.Address == "" {
				f.Name, f.Address = parseAddress(value)
			}
		case "subject":
			if f.Subject == "" {
				f.Subject = value
			}
		}
	}
	if f.Address == "" {
		return nil
	}
	return f
}

func parseAddress(v string) (name, address string) {
	clean := strings.ReplaceAll(v, "*", "")
	if a, err := mail.ParseAddress(clean); err == nil {
		return strings.TrimSpace(a.Name), strings.ToLower(a.Address)
	}
	if m := angleAddr.FindStringSubmatch(clean); m != nil {
		name = strings.Trim(strings.TrimSpace(strings.Split(clean, "<")[0]), `"`)
		return name, strings.ToLower(m[1])
	}
	if m := bareAddr.FindString(strings.ToLower(clean)); m != "" {
		return "", strings.Trim(m, "[]().,;:")
	}
	return "", ""
}
