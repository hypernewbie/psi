package testutil

// VTModel is a minimal xterm-compatible screen model for assertions.
// It tracks cells, scrollback, cursor position, modes, and erase operations.
// It is a test model, not a terminal emulator: it supports the subset psi
// emits (SGR stripped by sanitizer, CSI cursor/erase, CRLF, scrollback).
type VTModel struct {
	Width, Height int
	Rows          [][]rune // visible rows
	Widths        [][]int  // cell widths per rune (1 or 2)
	CursorRow     int
	CursorCol     int // in cells
	Scrollback    []string
	EraseOps      []string
	WriteCalls    int
	ByteCount     int
	FrameCount    int
	Modes         map[string]bool // e.g. "2004h" bracketed paste
	CursorVisible bool
}

func NewVTModel(w, h int) *VTModel {
	if w < 2 {
		w = 80
	}
	if h < 2 {
		h = 24
	}
	m := &VTModel{Width: w, Height: h, CursorVisible: true, Modes: map[string]bool{}}
	m.Clear()
	return m
}

func (m *VTModel) Clear() {
	m.Rows = make([][]rune, m.Height)
	m.Widths = make([][]int, m.Height)
	for i := range m.Rows {
		m.Rows[i] = []rune{}
		m.Widths[i] = []int{}
	}
	m.CursorRow, m.CursorCol = 0, 0
}

// rowWidth returns used cells in a row.
func (m *VTModel) rowWidth(r int) int {
	w := 0
	for _, cw := range m.Widths[r] {
		w += cw
	}
	return w
}

// PutText appends printable text at the cursor (no ANSI; caller sanitizes).
func (m *VTModel) PutText(s string) {
	for _, r := range s {
		if r == '\n' {
			m.newline()
			continue
		}
		if r == '\r' {
			m.CursorCol = 0
			continue
		}
		w := 1
		if rw := runeCellWidth(r); rw == 2 {
			w = 2
		}
		if m.CursorCol+w > m.Width {
			m.newline()
		}
		m.Rows[m.CursorRow] = append(m.Rows[m.CursorRow], r)
		m.Widths[m.CursorRow] = append(m.Widths[m.CursorRow], w)
		m.CursorCol += w
	}
}

func (m *VTModel) newline() {
	m.CursorRow++
	m.CursorCol = 0
	if m.CursorRow >= m.Height {
		// Scroll: top row enters scrollback.
		m.Scrollback = append(m.Scrollback, string(m.Rows[0]))
		copy(m.Rows, m.Rows[1:])
		copy(m.Widths, m.Widths[1:])
		m.Rows[m.Height-1] = []rune{}
		m.Widths[m.Height-1] = []int{}
		m.CursorRow = m.Height - 1
	}
}

// EraseDisplay records an erase op; ED 2/J clears scrollback (forbidden in normal path).
func (m *VTModel) Erase(op string) {
	m.EraseOps = append(m.EraseOps, op)
}

// Text returns visible text joined by newlines (for substring-free assertions use Cells).
func (m *VTModel) Text() string {
	rows := make([]string, len(m.Rows))
	for i, r := range m.Rows {
		rows[i] = string(r)
	}
	s := ""
	for i, r := range rows {
		if i > 0 {
			s += "\n"
		}
		s += r
	}
	return s
}

// Feed parses a byte stream with a small CSI subset and records ops.
func (m *VTModel) Feed(b []byte) {
	m.WriteCalls++
	m.ByteCount += len(b)
	i := 0
	for i < len(b) {
		if b[i] == 0x1b && i+1 < len(b) && b[i+1] == '[' {
			j := i + 2
			for j < len(b) && (b[j] < 0x40 || b[j] > 0x7e) {
				j++
			}
			if j >= len(b) {
				break
			}
			final := b[j]
			params := string(b[i+2 : j])
			switch final {
			case 'A':
				m.CursorRow -= atoi(params, 1)
				if m.CursorRow < 0 {
					m.CursorRow = 0
				}
			case 'B':
				m.CursorRow += atoi(params, 1)
				if m.CursorRow >= m.Height {
					m.CursorRow = m.Height - 1
				}
			case 'C':
				m.CursorCol += atoi(params, 1)
			case 'D':
				m.CursorCol -= atoi(params, 1)
				if m.CursorCol < 0 {
					m.CursorCol = 0
				}
			case 'H', 'f':
				m.CursorRow, m.CursorCol = 0, 0
			case 'J':
				m.Erase("ED" + params)
			case 'K':
				m.Erase("EL" + params)
			case 'h', 'l':
				m.Modes[params] = final == 'h'
			}
			i = j + 1
			continue
		}
		// printable run
		j := i
		for j < len(b) && b[j] != 0x1b {
			j++
		}
		m.PutText(string(b[i:j]))
		i = j
	}
}

func atoi(s string, def int) int {
	n := 0
	neg := false
	for _, c := range s {
		if c == '-' {
			neg = true
			continue
		}
		if c < '0' || c > '9' {
			continue
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		n = -n
	}
	if n == 0 {
		return def
	}
	return n
}

func runeCellWidth(r rune) int {
	if r < 0x1100 {
		return 1
	}
	// Conservative wide check for the model (mirrors go-runewidth for tests).
	if r >= 0x1100 && r <= 0x115F {
		return 2
	}
	if r >= 0x2E80 && r <= 0x303E {
		return 2
	}
	if r >= 0x3041 && r <= 0x33FF {
		return 2
	}
	if r >= 0x3400 && r <= 0x4DBF {
		return 2
	}
	if r >= 0x4E00 && r <= 0x9FFF {
		return 2
	}
	if r >= 0xAC00 && r <= 0xD7A3 {
		return 2
	}
	if r >= 0xFF00 && r <= 0xFF60 {
		return 2
	}
	if r >= 0x1F300 && r <= 0x1FAFF {
		return 2
	}
	return 1
}
