package run

import (
	"context"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestArgvKeepsInputsOutOfScriptSource(t *testing.T) {
	hostile := `x" & do shell script "rm -rf ~" & "`
	for _, c := range []Command{
		{Kind: "trash", Target: hostile},
		{Kind: "quit_app", Target: hostile},
		{Kind: "quit_app_named", Target: hostile},
		{Kind: "argv", Admin: true, Argv: []string{"/usr/sbin/purge", hostile}},
	} {
		argv := Argv(c)
		if argv[0] != osascript {
			t.Errorf("%s: argv[0] = %q", c.Kind, argv[0])
		}
		sep := -1
		for i, a := range argv {
			if a == "--" {
				sep = i
			}
		}
		if sep < 0 {
			t.Fatalf("%s: no -- separator in %v", c.Kind, argv)
		}
		for _, a := range argv[:sep] {
			if strings.Contains(a, hostile) {
				t.Errorf("%s: input spliced into script text: %q", c.Kind, a)
			}
		}
		if !strings.Contains(strings.Join(argv[sep+1:], "\x00"), hostile) {
			t.Errorf("%s: input missing from arguments after --", c.Kind)
		}
	}
}

func TestArgvPlainCommandIsVerbatim(t *testing.T) {
	c := Command{Kind: "argv", Argv: []string{"/usr/bin/killall", "-HUP", "Dock"}}
	got := Argv(c)
	if strings.Join(got, " ") != "/usr/bin/killall -HUP Dock" {
		t.Errorf("argv %v", got)
	}
}

func TestDisplayQuotesOnlyWhatNeedsIt(t *testing.T) {
	cases := []struct {
		c    Command
		want string
	}{
		{Command{Kind: "argv", Argv: []string{"/bin/rm", "-rf", "/Users/me/My Files", ""}}, `/bin/rm -rf "/Users/me/My Files" ""`},
		{Command{Kind: "argv", Admin: true, Argv: []string{"/usr/sbin/purge"}}, "/usr/sbin/purge   (as administrator — macOS asks for your password)"},
		{Command{Kind: "trash", Target: "/Users/me/old.dmg"}, "Finder: move to Trash /Users/me/old.dmg"},
		{Command{Kind: "quit_app", Target: "com.apple.Music"}, "quit app id com.apple.Music"},
		{Command{Kind: "quit_app_named", Target: "Music"}, "quit app Music"},
	}
	for _, c := range cases {
		if got := c.c.Display(); got != c.want {
			t.Errorf("Display(%+v) = %q, want %q", c.c, got, c.want)
		}
	}
}

func TestRunRefusesRelativeAndEmptyPrograms(t *testing.T) {
	for _, argv := range [][]string{nil, {}, {"rm", "-rf", "x"}, {"./evil"}} {
		if _, err := (OS{}).Run(context.Background(), Command{Kind: "argv", Argv: argv}); err == nil {
			t.Errorf("%v: ran", argv)
		}
	}
}

func TestTruncate(t *testing.T) {
	long := strings.Repeat("é", 100) // 2 bytes each
	for _, n := range []int{10, 11} {
		got := truncate(long, n)
		if !utf8.ValidString(got) || !strings.HasSuffix(got, "…") || len(got) > n+len("…") {
			t.Errorf("truncate(_, %d) = %q", n, got)
		}
	}
	if truncate("short", 10) != "short" {
		t.Error("short strings must be untouched")
	}
}
