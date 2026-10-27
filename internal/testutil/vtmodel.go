package testutil

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

// VTModel interprets the cursor/erase subset emitted by psi. It is not evidence
// for the complete terminal matrix. In particular, it must overwrite cells and
// apply erases: merely recording these operations hides destructive redraws.
type VTModel struct {
	Width, Height int
	Rows          [][]rune
	CursorRow     int
	CursorCol     int
	Scrollback    []string
	EraseOps      []string
	WriteCalls    int
	ByteCount     int
	FrameCount    int
	Modes         map[string]bool
	CursorVisible bool
	pending       []byte
	wrap          bool
}

func NewVTModel(w, h int) *VTModel {
	m := &VTModel{Width: max(1, w), Height: max(1, h), CursorVisible: true, Modes: map[string]bool{}}
	m.Clear()
	return m
}

func (m *VTModel) Clear() {
	m.Rows = make([][]rune, m.Height)
	for i := range m.Rows {
		m.Rows[i] = make([]rune, m.Width)
	}
	m.CursorRow, m.CursorCol, m.wrap = 0, 0, false
}

func rowText(row []rune) string {
	var b strings.Builder
	for _, r := range row {
		if r == 0 {
			b.WriteByte(' ')
		} else {
			b.WriteRune(r)
		}
	}
	return strings.TrimRight(b.String(), " ")
}

func (m *VTModel) newline() {
	m.wrap = false
	m.CursorRow++
	if m.CursorRow >= m.Height {
		m.Scrollback = append(m.Scrollback, rowText(m.Rows[0]))
		copy(m.Rows, m.Rows[1:])
		m.Rows[m.Height-1] = make([]rune, m.Width)
		m.CursorRow = m.Height - 1
	}
}

func (m *VTModel) PutText(s string) {
	for _, r := range s {
		switch r {
		case '\r':
			m.CursorCol, m.wrap = 0, false
			continue
		case '\n':
			m.newline()
			continue
		case '\t':
			m.CursorCol = min(m.Width-1, (m.CursorCol/8+1)*8)
			continue
		}
		width := uniseg.StringWidth(string(r))
		if width == 0 || r < 32 {
			continue
		}
		width = min(width, m.Width)
		if m.wrap || m.CursorCol+width > m.Width {
			m.CursorCol = 0
			m.newline()
		}
		m.Rows[m.CursorRow][m.CursorCol] = r
		for i := 1; i < width; i++ {
			m.Rows[m.CursorRow][m.CursorCol+i] = 0
		}
		m.CursorCol += width
		if m.CursorCol == m.Width {
			m.CursorCol--
			m.wrap = true
		}
	}
}

func (m *VTModel) Text() string {
	rows := make([]string, len(m.Rows))
	for i, r := range m.Rows {
		rows[i] = rowText(r)
	}
	return strings.Join(rows, "\n")
}

func (m *VTModel) Feed(b []byte) {
	m.WriteCalls++
	m.ByteCount += len(b)
	m.pending = append(m.pending, b...)
	for len(m.pending) > 0 {
		p := m.pending
		if p[0] == 27 {
			if len(p) < 2 {
				return
			}
			if p[1] != '[' {
				m.pending = p[2:]
				continue
			}
			j := 2
			for j < len(p) && (p[j] < 0x40 || p[j] > 0x7e) {
				j++
			}
			if j == len(p) {
				return
			}
			m.csi(string(p[2:j]), p[j])
			m.pending = p[j+1:]
			continue
		}
		if !utf8.FullRune(p) {
			return
		}
		r, n := utf8.DecodeRune(p)
		m.PutText(string(r))
		m.pending = p[n:]
	}
}

func (m *VTModel) csi(params string, final byte) {
	parts := strings.Split(params, ";")
	param := func(i, fallback int) int {
		if i >= len(parts) {
			return fallback
		}
		n, err := strconv.Atoi(parts[i])
		if err != nil || n == 0 {
			return fallback
		}
		return n
	}
	n := param(0, 1)
	m.wrap = false
	switch final {
	case 'A':
		m.CursorRow = max(0, m.CursorRow-n)
	case 'B':
		m.CursorRow = min(m.Height-1, m.CursorRow+n)
	case 'C':
		m.CursorCol = min(m.Width-1, m.CursorCol+n)
	case 'D':
		m.CursorCol = max(0, m.CursorCol-n)
	case 'H', 'f':
		m.CursorRow = min(m.Height-1, param(0, 1)-1)
		m.CursorCol = min(m.Width-1, param(1, 1)-1)
	case 'K':
		m.EraseOps = append(m.EraseOps, "EL"+params)
		mode := param(0, 0)
		start, end := m.CursorCol, m.Width
		if mode == 1 {
			start, end = 0, m.CursorCol+1
		} else if mode == 2 {
			start = 0
		}
		clear(m.Rows[m.CursorRow][start:end])
	case 'J':
		m.EraseOps = append(m.EraseOps, "ED"+params)
		mode := param(0, 0)
		if mode == 3 {
			m.Scrollback = nil
			return
		}
		for r := range m.Rows {
			start, end := 0, m.Width
			if mode == 0 {
				if r < m.CursorRow {
					continue
				}
				if r == m.CursorRow {
					start = m.CursorCol
				}
			} else if mode == 1 {
				if r > m.CursorRow {
					continue
				}
				if r == m.CursorRow {
					end = m.CursorCol + 1
				}
			}
			clear(m.Rows[r][start:end])
		}
	case 'h', 'l':
		m.Modes[params] = final == 'h'
		if params == "?25" {
			m.CursorVisible = final == 'h'
		}
	}
}
