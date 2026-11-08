package shell

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

const (
	up    = "\x1b[A"
	down  = "\x1b[B"
	right = "\x1b[C"
	left  = "\x1b[D"
	del   = "\x1b[3~"
)

func newEd(input string) (*Editor, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return &Editor{In: bufio.NewReader(strings.NewReader(input)), Out: out}, out
}

func read(t *testing.T, input string, hist ...string) string {
	t.Helper()
	e, _ := newEd(input)
	e.History = hist
	line, err := e.ReadLine("> ")
	if err != nil {
		t.Fatalf("%q: %v", input, err)
	}
	return line
}

func TestEditorEditing(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"plain", "hello\r", "hello"},
		{"newline also accepts", "hi\n", "hi"},
		{"backspace", "abc\x7fd\r", "abd"},
		{"backspace 0x08", "abc\x08d\r", "abd"},
		{"insert in the middle", "ac" + left + "b\r", "abc"},
		{"right stops at end", "ab" + right + right + "c\r", "abc"},
		{"home/end", "bc\x01a\x05d\r", "abcd"},
		{"home/end keys", "bc\x1b[Ha\x1b[Fd\r", "abcd"},
		{"home/end tilde", "bc\x1b[1~a\x1b[4~d\r", "abcd"},
		{"ctrl-b/f", "ac\x02b\x06d\r", "abcd"},
		{"delete key", "abc\x01" + del + "\r", "bc"},
		{"ctrl-d deletes under cursor", "abc\x01\x04\r", "bc"},
		{"ctrl-d at end of text does nothing", "abc\x04\r", "abc"},
		{"ctrl-u", "abc\x15x\r", "x"},
		{"ctrl-w", "one two\x17\r", "one "},
		{"ctrl-w twice", "one two\x17\x17\r", ""},
		{"alt-b / alt-f", "one two\x1bbX\x1bfY\r", "one Xtwo" + "Y"},
		{"ctrl-left/right", "one two" + "\x1b[1;5D" + "X" + "\x1b[1;5C" + "Y\r", "one XtwoY"},
		{"alt-d kills the word ahead", "one two\x01\x1bd\r", " two"},
		{"alt-backspace kills the word back", "one two\x1b\x7f\r", "one "},
		{"ctrl-l keeps the line", "ab\x0cc\r", "abc"},
		{"unicode backspace removes one rune", "za\u017c\u00f3\u0142\u0107\x7f\r", "za\u017c\u00f3\u0142"},
		{"unicode insert", "a\u0107" + left + "\u017c\r", "a\u017c\u0107"},
		{"unknown escape sequence is ignored", "a\x1b[Zb\r", "ab"},
		{"control characters are not inserted", "a\x00\x07b\r", "ab"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := read(t, tc.in); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

func TestEditorKillAndYank(t *testing.T) {
	// "hello world": cursor after "hello", kill the rest, yank it back twice
	in := "hello world\x01" + strings.Repeat(right, 5) + "\x0b\x19\x19\r"
	if got := read(t, in); got != "hello world world" {
		t.Errorf("got %q", got)
	}
	// ctrl-u puts the text in the kill buffer too
	if got := read(t, "abc\x15\x19\x19\r"); got != "abcabc" {
		t.Errorf("got %q", got)
	}
}

func TestEditorEndsAndInterrupts(t *testing.T) {
	e, _ := newEd("\x04")
	if _, err := e.ReadLine("> "); err != io.EOF {
		t.Errorf("ctrl-d on an empty line: %v", err)
	}
	e, out := newEd("abc\x03")
	if _, err := e.ReadLine("> "); !errors.Is(err, ErrInterrupt) || !strings.Contains(out.String(), "^C") {
		t.Errorf("ctrl-c: %v %q", err, out.String())
	}
	e, _ = newEd("")
	if _, err := e.ReadLine("> "); err != io.EOF {
		t.Errorf("closed input: %v", err)
	}
	// input closed in the middle of a line: the text is not lost
	e, _ = newEd("abc")
	if l, err := e.ReadLine("> "); err != nil || l != "abc" {
		t.Errorf("partial line: %q %v", l, err)
	}
}

func TestEditorHistory(t *testing.T) {
	h := []string{"one", "two", "three"}
	for _, tc := range []struct{ name, in, want string }{
		{"up", up + "\r", "three"},
		{"up up", up + up + "\r", "two"},
		{"up stops at the oldest", strings.Repeat(up, 10) + "\r", "one"},
		{"up down", up + up + down + "\r", "three"},
		{"down past the newest gives the empty line", up + down + "\r", ""},
		{"ctrl-p / ctrl-n", "\x10\x10\x0e\r", "three"},
		{"the line being typed is kept", "xy" + up + up + down + down + "\r", "xy"},
		{"recalled lines are editable", up + "\x7f" + "X\r", "threX"},
		{"down with nothing newer does nothing", down + "ab\r", "ab"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := read(t, tc.in, h...); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
	// a second ReadLine starts again at the newest entry
	e, _ := newEd(up + "\r" + up + up + "\r")
	e.History = []string{"a", "b"}
	if l, _ := e.ReadLine("> "); l != "b" {
		t.Errorf("first: %q", l)
	}
	if l, _ := e.ReadLine("> "); l != "a" {
		t.Errorf("second: %q", l)
	}
}

func TestEditorAddHistory(t *testing.T) {
	e, _ := newEd("")
	e.Skip = func(l string) bool { return strings.Contains(l, "password=") }
	for _, l := range []string{"one", "one", "", "  ", "add password=secret", "two", "one"} {
		e.AddHistory(l)
	}
	if strings.Join(e.History, "|") != "one|two|one" {
		t.Errorf("history: %q", e.History)
	}
}

func TestEditorReverseSearch(t *testing.T) {
	h := []string{"add chain=input", "set 0 disabled=yes", "add chain=forward", "print"}
	for _, tc := range []struct{ name, in, want string }{
		{"finds the newest match and runs it", "\x12chain\r", "add chain=forward"},
		{"ctrl-r again goes further back", "\x12chain\x12\r", "add chain=input"},
		{"narrowing the query", "\x12ch\x7f\x7f\x7fdis\r", "set 0 disabled=yes"},
		{"cancel restores the typed text", "ab\x12chain\x07c\r", "abc"},
		{"ctrl-c cancels the search only", "ab\x12chain\x03c\r", "abc"},
		{"no match then enter keeps the text", "ab\x12zzz\rc\r", "abc"},
		{"another control key leaves the match for editing", "\x12print\x05!\r", "print!"},
		{"arrow key accepts the match", "\x12disabled" + left + "X\r", "set 0 disabled=yeXs"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := read(t, tc.in, h...); got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
	// the search prompt is drawn
	e, out := newEd("\x12chain\r")
	e.History = h
	e.ReadLine("> ")
	if !strings.Contains(out.String(), "(reverse-i-search)`chain'") {
		t.Errorf("no search prompt in %q", out.String())
	}
}

func TestEditorTab(t *testing.T) {
	cands := map[string][]Candidate{
		"fi":    {{"filter", " ", ""}},
		"ad":    {{"add", " ", ""}, {"address-list", " ", ""}},
		"nat":   {{"nat-a", "", ""}, {"nat-b", "", ""}},
		"a":     {{"alpha", "", ""}, {"beta", "", ""}},
		"x=":    nil,
		"set a": {{"abc", "=", ""}},
	}
	mk := func(in string) (string, string) {
		e, out := newEd(in)
		e.Complete = func(line string) (int, []Candidate) {
			if c, ok := cands[line]; ok {
				return len([]rune(line)) - len([]rune(line[strings.LastIndex(line, " ")+1:])), c
			}
			return 0, nil
		}
		l, err := e.ReadLine("> ")
		if err != nil {
			t.Fatal(err)
		}
		return l, out.String()
	}
	// unique candidate: completed with its suffix
	if l, _ := mk("fi\t\r"); l != "filter " {
		t.Errorf("unique: %q", l)
	}
	// parameter: the "=" suffix, cursor stays right behind it
	if l, _ := mk("set a\tb\r"); l != "set abc=b" {
		t.Errorf("param: %q", l)
	}
	// several candidates sharing a longer prefix: the prefix is inserted, no list yet
	if l, out := mk("ad\t\r"); l != "add" || strings.Contains(out, "address-list") {
		t.Errorf("common prefix: %q out=%q", l, out)
	}
	if l, out := mk("nat\t\r"); l != "nat-" || strings.Contains(out, "nat-b") {
		t.Errorf("prefix: %q %q", l, out)
	}
	// nothing more to add: the candidates are listed and the text stays
	if l, out := mk("a\t\r"); l != "a" || !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Errorf("list: %q out=%q", l, out)
	}
	// nothing to complete rings the bell and keeps the text
	if l, out := mk("x=\t\r"); l != "x=" || !strings.Contains(out, "\a") {
		t.Errorf("bell: %q %q", l, out)
	}
	// without a Complete function Tab just rings the bell
	e, out := newEd("ab\tc\r")
	if l, _ := e.ReadLine("> "); l != "abc" || !strings.Contains(out.String(), "\a") {
		t.Errorf("no completer: %q", l)
	}
}

func TestEditorHelpKey(t *testing.T) {
	var asked []string
	mk := func(in string) (string, string) {
		e, out := newEd(in)
		e.Help = func(line string) []Candidate {
			asked = append(asked, line)
			return []Candidate{{"chain", "", "string"}, {"action", "", "accept|drop"}}
		}
		l, _ := e.ReadLine("> ")
		return l, out.String()
	}
	// "?" shows the help for the text before the cursor and is not inserted
	l, out := mk("add ?\r")
	if l != "add " || len(asked) != 1 || asked[0] != "add " || !strings.Contains(out, "accept|drop") {
		t.Errorf("help: %q asked=%q out=%q", l, asked, out)
	}
	// inside an open quote it is a literal character
	asked = nil
	if l, _ := mk("add comment=\"why?\"\r"); l != `add comment="why?"` || len(asked) != 0 {
		t.Errorf("quote: %q asked=%q", l, asked)
	}
	// nothing to show: bell and no insert
	e, out2 := newEd("a?\r")
	e.Help = func(string) []Candidate { return nil }
	if l, _ := e.ReadLine("> "); l != "a" || !strings.Contains(out2.String(), "\a") {
		t.Errorf("empty help: %q", l)
	}
}

func TestEditorRedrawGeometry(t *testing.T) {
	// a line longer than the terminal wraps: it must still be returned intact
	e, out := newEd(strings.Repeat("x", 45) + "\x01Y\r")
	e.Width = func() int { return 20 }
	l, err := e.ReadLine("> ")
	if err != nil || l != "Y"+strings.Repeat("x", 45) {
		t.Fatalf("%q %v", l, err)
	}
	// moving up over wrapped rows and clearing the screen below is part of the redraw
	if !strings.Contains(out.String(), "\x1b[2A") || !strings.Contains(out.String(), "\x1b[J") {
		t.Errorf("expected multi-row redraw sequences in %q", out.String())
	}
	// the cursor sits at the right column after the prompt on an empty line
	e, out = newEd("\r")
	e.ReadLine("[ffnvr] / > ")
	if !strings.Contains(out.String(), "[ffnvr] / > ") || !strings.Contains(out.String(), "\x1b[12C") {
		t.Errorf("prompt/cursor: %q", out.String())
	}
	// exactly full row: the forced wrap keeps the cursor on the next row
	e, out = newEd(strings.Repeat("y", 18) + "\r")
	e.Width = func() int { return 20 }
	if l, _ := e.ReadLine("> "); l != strings.Repeat("y", 18) {
		t.Errorf("full row: %q", l)
	}
	if !strings.Contains(out.String(), "\r\n") {
		t.Errorf("expected the forced wrap, got %q", out.String())
	}
}

func TestEditorRawHook(t *testing.T) {
	called, restored := 0, 0
	e, _ := newEd("a\r")
	e.Raw = func() (func(), error) { called++; return func() { restored++ }, nil }
	e.ReadLine("> ")
	if called != 1 || restored != 1 {
		t.Errorf("raw mode must be entered and left once per line: %d/%d", called, restored)
	}
	e, _ = newEd("a\r")
	e.Raw = func() (func(), error) { return nil, errors.New("no tty") }
	if _, err := e.ReadLine("> "); err == nil {
		t.Error("a failing raw mode must be reported")
	}
}
