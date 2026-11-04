package textutil

import "testing"

func TestClean(t *testing.T) {
	for in, want := range map[string]string{
		"ciao\nmondo\t!":                 "ciao\nmondo\t!",
		"\x1b]52;c;cm0gLXJmIH4=\x07paid": "]52;c;cm0gLXJmIH4=paid",
		"\x1b[2J\x1b[31mred":             "[2J[31mred",
		"\u009b31mcsi":                   "31mcsi",
		"invoice‮txt.exe":                "invoicetxt.exe",
		"città · €":                      "città · €",
		"del\x7fete":                     "delete",
	} {
		if got := Clean(in); got != want {
			t.Errorf("Clean(%q) = %q, want %q", in, got, want)
		}
	}
	if got := Line("a\nb\tc\x1b"); got != "a b c" {
		t.Errorf("Line: %q", got)
	}
}
