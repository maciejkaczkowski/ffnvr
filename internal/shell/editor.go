package shell

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
)

// ErrInterrupt is returned by ReadLine when the user pressed Ctrl-C.
var ErrInterrupt = errors.New("interrupted")

// Editor is a small readline: line editing, history, reverse search and completion.
// It talks to a terminal in raw mode (see Raw) and redraws the line after every key.
type Editor struct {
	In  *bufio.Reader
	Out io.Writer

	History  []string
	Complete func(line string) (start int, cands []Candidate) // line = text before the cursor
	Help     func(line string) []Candidate
	// Raw switches the terminal to raw mode for one ReadLine call (nil: input is already raw/plain).
	Raw   func() (restore func(), err error)
	Width func() int // terminal columns, default 80
	// Skip tells which lines must not enter the history (e.g. passwords).
	Skip func(line string) bool

	kill   []rune
	curRow int // row of the cursor, relative to the first row of the prompt
	rows   int // rows used by the last drawing
}

type key int

const (
	keyNone key = iota
	keyUp
	keyDown
	keyLeft
	keyRight
	keyHome
	keyEnd
	keyDelete
	keyWordLeft
	keyWordRight
	keyKillWordFwd
	keyKillWordBack
)

// AddHistory remembers an accepted line (no empty lines, no immediate duplicates, no secrets).
func (e *Editor) AddHistory(line string) {
	if strings.TrimSpace(line) == "" || (e.Skip != nil && e.Skip(line)) {
		return
	}
	if n := len(e.History); n > 0 && e.History[n-1] == line {
		return
	}
	e.History = append(e.History, line)
}

func (e *Editor) width() int {
	if e.Width != nil {
		if w := e.Width(); w > 10 {
			return w
		}
	}
	return 80
}

func (e *Editor) write(s string) { io.WriteString(e.Out, s) }

// refresh redraws prompt and buffer and puts the cursor at pos. Long lines wrap
// over several rows, so the cursor first goes back to the top row.
func (e *Editor) refresh(prompt string, buf []rune, pos int) {
	cols := e.width()
	plen := len([]rune(prompt))
	var sb strings.Builder
	if e.curRow > 0 {
		fmt.Fprintf(&sb, "\x1b[%dA", e.curRow)
	}
	sb.WriteString("\r\x1b[J")
	sb.WriteString(prompt)
	sb.WriteString(string(buf))
	total := plen + len(buf)
	if len(buf) > 0 && total%cols == 0 { // the terminal waits at the last column: force the wrap
		sb.WriteString("\r\n")
	}
	endRow := total / cols
	target := plen + pos
	trow, tcol := target/cols, target%cols
	if endRow > trow {
		fmt.Fprintf(&sb, "\x1b[%dA", endRow-trow)
	}
	sb.WriteString("\r")
	if tcol > 0 {
		fmt.Fprintf(&sb, "\x1b[%dC", tcol)
	}
	e.write(sb.String())
	e.curRow, e.rows = trow, endRow+1
}

// newline moves below the edited line.
func (e *Editor) newline() {
	if d := e.rows - 1 - e.curRow; d > 0 {
		e.write(fmt.Sprintf("\x1b[%dB", d))
	}
	e.write("\r\n")
	e.curRow, e.rows = 0, 1
}

func (e *Editor) readEscape() key {
	if e.In.Buffered() == 0 {
		return keyNone // a lone Esc
	}
	r, _, err := e.In.ReadRune()
	if err != nil {
		return keyNone
	}
	switch r {
	case '[':
		var params []rune
		for {
			c, _, err := e.In.ReadRune()
			if err != nil {
				return keyNone
			}
			if c >= 0x40 && c <= 0x7e {
				p := string(params)
				ctrl := strings.HasSuffix(p, ";5") || strings.HasSuffix(p, ";3")
				switch c {
				case 'A':
					return keyUp
				case 'B':
					return keyDown
				case 'C':
					if ctrl {
						return keyWordRight
					}
					return keyRight
				case 'D':
					if ctrl {
						return keyWordLeft
					}
					return keyLeft
				case 'H':
					return keyHome
				case 'F':
					return keyEnd
				case '~':
					switch strings.SplitN(p, ";", 2)[0] {
					case "1", "7":
						return keyHome
					case "4", "8":
						return keyEnd
					case "3":
						return keyDelete
					}
				}
				return keyNone
			}
			params = append(params, c)
		}
	case 'O':
		c, _, _ := e.In.ReadRune()
		switch c {
		case 'H':
			return keyHome
		case 'F':
			return keyEnd
		case 'A':
			return keyUp
		case 'B':
			return keyDown
		case 'C':
			return keyRight
		case 'D':
			return keyLeft
		}
	case 'b':
		return keyWordLeft
	case 'f':
		return keyWordRight
	case 'd':
		return keyKillWordFwd
	case 127, 8:
		return keyKillWordBack
	}
	return keyNone
}

func wordLeft(buf []rune, pos int) int {
	for pos > 0 && unicode.IsSpace(buf[pos-1]) {
		pos--
	}
	for pos > 0 && !unicode.IsSpace(buf[pos-1]) {
		pos--
	}
	return pos
}

func wordRight(buf []rune, pos int) int {
	for pos < len(buf) && unicode.IsSpace(buf[pos]) {
		pos++
	}
	for pos < len(buf) && !unicode.IsSpace(buf[pos]) {
		pos++
	}
	return pos
}

func replace(buf []rune, from, to int, with []rune) ([]rune, int) {
	out := append(append(append([]rune{}, buf[:from]...), with...), buf[to:]...)
	return out, from + len(with)
}

func commonPrefix(cands []Candidate) string {
	p := cands[0].Text
	for _, c := range cands[1:] {
		for !strings.HasPrefix(c.Text, p) {
			p = p[:len(p)-1]
		}
	}
	return p
}

// list prints candidates below the line, in columns, or with descriptions.
func (e *Editor) list(cands []Candidate, withHelp bool) {
	e.newline()
	const max = 60
	more := 0
	if len(cands) > max {
		more, cands = len(cands)-max, cands[:max]
	}
	w := 0
	for _, c := range cands {
		if n := len([]rune(c.Text)); n > w {
			w = n
		}
	}
	if withHelp {
		for _, c := range cands {
			if c.Help != "" {
				e.write(fmt.Sprintf("  %-*s  %s\r\n", w, c.Text, c.Help))
			} else {
				e.write("  " + c.Text + "\r\n")
			}
		}
	} else {
		cols := e.width() / (w + 2)
		if cols < 1 {
			cols = 1
		}
		for i, c := range cands {
			e.write(fmt.Sprintf("%-*s  ", w, c.Text))
			if (i+1)%cols == 0 || i == len(cands)-1 {
				e.write("\r\n")
			}
		}
	}
	if more > 0 {
		e.write(fmt.Sprintf("  ... %d more, keep typing\r\n", more))
	}
}

// ReadLine reads one line. It returns io.EOF on Ctrl-D at an empty line and
// ErrInterrupt on Ctrl-C.
func (e *Editor) ReadLine(prompt string) (string, error) {
	if e.Raw != nil {
		restore, err := e.Raw()
		if err != nil {
			return "", err
		}
		defer restore()
	}
	var buf []rune
	pos := 0
	hist := len(e.History)
	var saved []rune
	e.curRow, e.rows = 0, 1
	e.refresh(prompt, buf, pos)

	setFromHistory := func(i int) {
		hist = i
		if i == len(e.History) {
			buf = append([]rune{}, saved...)
		} else {
			buf = []rune(e.History[i])
		}
		pos = len(buf)
	}

	for {
		r, _, err := e.In.ReadRune()
		if err != nil {
			e.newline()
			if len(buf) == 0 {
				return "", io.EOF
			}
			return string(buf), nil
		}
		k := keyNone
		switch r {
		case '\r', '\n':
			e.newline()
			return string(buf), nil
		case 3:
			e.write("^C")
			e.newline()
			return "", ErrInterrupt
		case 4:
			if len(buf) == 0 {
				e.newline()
				return "", io.EOF
			}
			k = keyDelete
		case 1:
			k = keyHome
		case 5:
			k = keyEnd
		case 2:
			k = keyLeft
		case 6:
			k = keyRight
		case 16:
			k = keyUp
		case 14:
			k = keyDown
		case 11: // kill to end of line
			e.kill = append([]rune{}, buf[pos:]...)
			buf = buf[:pos]
		case 21: // kill to start of line
			e.kill = append([]rune{}, buf[:pos]...)
			buf, pos = replace(buf, 0, pos, nil)
		case 23:
			k = keyKillWordBack
		case 25: // yank
			buf, pos = replace(buf, pos, pos, e.kill)
		case 12:
			e.write("\x1b[H\x1b[2J")
			e.curRow = 0
		case 127, 8:
			if pos > 0 {
				buf, pos = replace(buf, pos-1, pos, nil)
			}
		case 27:
			k = e.readEscape()
		case 9: // Tab
			start, cands := e.complete(string(buf[:pos]))
			switch {
			case len(cands) == 0:
				e.write("\a")
			case len(cands) == 1:
				buf, pos = replace(buf, start, pos, []rune(cands[0].Text+cands[0].Suffix))
			default:
				if cp := commonPrefix(cands); len([]rune(cp)) > pos-start {
					buf, pos = replace(buf, start, pos, []rune(cp))
				} else {
					e.list(cands, false)
				}
			}
		case 18: // Ctrl-R
			line, execute, pending := e.search(&buf, &pos)
			if execute {
				e.newline()
				return line, nil
			}
			k = pending
		case '?':
			if e.Help != nil && !inOpenQuote(string(buf[:pos])) {
				if h := e.Help(string(buf[:pos])); len(h) > 0 {
					e.list(h, true)
				} else {
					e.write("\a")
				}
			} else {
				buf, pos = replace(buf, pos, pos, []rune{r})
			}
		default:
			if r >= 32 {
				buf, pos = replace(buf, pos, pos, []rune{r})
			}
		}
		switch k {
		case keyUp:
			if hist > 0 {
				if hist == len(e.History) {
					saved = append([]rune{}, buf...)
				}
				setFromHistory(hist - 1)
			}
		case keyDown:
			if hist < len(e.History) {
				setFromHistory(hist + 1)
			}
		case keyLeft:
			if pos > 0 {
				pos--
			}
		case keyRight:
			if pos < len(buf) {
				pos++
			}
		case keyHome:
			pos = 0
		case keyEnd:
			pos = len(buf)
		case keyDelete:
			if pos < len(buf) {
				buf, _ = replace(buf, pos, pos+1, nil)
			}
		case keyWordLeft:
			pos = wordLeft(buf, pos)
		case keyWordRight:
			pos = wordRight(buf, pos)
		case keyKillWordBack:
			to := wordLeft(buf, pos)
			e.kill = append([]rune{}, buf[to:pos]...)
			buf, pos = replace(buf, to, pos, nil)
		case keyKillWordFwd:
			to := wordRight(buf, pos)
			e.kill = append([]rune{}, buf[pos:to]...)
			buf, _ = replace(buf, pos, to, nil)
		}
		e.refresh(prompt, buf, pos)
	}
}

func (e *Editor) complete(line string) (int, []Candidate) {
	if e.Complete == nil {
		return 0, nil
	}
	return e.Complete(line)
}

// search implements Ctrl-R. It returns the line and whether it must be executed
// at once (Enter); otherwise a found line is left in buf for further editing.
func (e *Editor) search(buf *[]rune, pos *int) (string, bool, key) {
	var query []rune
	match := -1
	find := func(from int) {
		for i := from; i >= 0; i-- {
			if i < len(e.History) && strings.Contains(e.History[i], string(query)) {
				match = i
				return
			}
		}
		match = -1
	}
	draw := func() {
		line := ""
		if match >= 0 {
			line = e.History[match]
		}
		e.refresh(fmt.Sprintf("(reverse-i-search)`%s': ", string(query)), []rune(line), len([]rune(line)))
	}
	orig, origPos := append([]rune{}, *buf...), *pos
	draw()
	for {
		r, _, err := e.In.ReadRune()
		if err != nil {
			return "", false, keyNone
		}
		switch {
		case r == '\r' || r == '\n':
			if match >= 0 {
				return e.History[match], true, keyNone
			}
			*buf, *pos = orig, origPos
			return "", false, keyNone
		case r == 3 || r == 7: // Ctrl-C / Ctrl-G
			*buf, *pos = orig, origPos
			return "", false, keyNone
		case r == 18:
			if match >= 0 {
				find(match - 1)
			}
		case r == 127 || r == 8:
			if len(query) > 0 {
				query = query[:len(query)-1]
				find(len(e.History) - 1)
			}
		case r == 27:
			k := e.readEscape()
			if k == keyNone && match < 0 {
				*buf, *pos = orig, origPos
				return "", false, keyNone
			}
			if match >= 0 {
				*buf = []rune(e.History[match])
				*pos = len(*buf)
			} else {
				*buf, *pos = orig, origPos
			}
			return "", false, k
		case r >= 32:
			query = append(query, r)
			start := len(e.History) - 1
			if match >= 0 {
				start = match
			}
			find(start)
		default: // any other control key: keep the match and apply the key
			if match >= 0 {
				*buf = []rune(e.History[match])
				*pos = len(*buf)
			} else {
				*buf, *pos = orig, origPos
			}
			return "", false, map[rune]key{1: keyHome, 5: keyEnd, 2: keyLeft, 6: keyRight}[r]
		}
		draw()
	}
}
