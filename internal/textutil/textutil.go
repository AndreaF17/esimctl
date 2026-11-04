// Package textutil cleans text that comes from outside the machine (SMS,
// operator and profile names, SM-DP+ metadata) before it is shown.
package textutil

import "strings"

// Clean drops characters that would let such text drive a terminal or
// disguise itself: C0/C1 control codes other than newline and tab (ANSI
// escape sequences start with ESC or CSI), DEL, and Unicode bidirectional
// overrides that reorder how text is displayed.
func Clean(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
			b.WriteRune(r)
		case r < 0x20, r == 0x7F, r >= 0x80 && r <= 0x9F:
			// control character: dropped
		case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0x200E, r == 0x200F:
			// bidi override/isolate/mark: dropped
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Line is Clean for single-line fields: newlines and tabs become spaces.
func Line(s string) string {
	return strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return ' '
		}
		return r
	}, Clean(s))
}
