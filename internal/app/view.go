package app

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"ytmg/internal/yt"
)

// --- palette -------------------------------------------------------------

var (
	accent     = lipgloss.Color("#FF0033")
	accentSoft = lipgloss.Color("#FF4D4D")
	cream      = lipgloss.Color("#FFD6DE")

	headStyle = lipgloss.NewStyle().Bold(true)
	dimStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	boxStyle  = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(accent).
			Padding(0, 1)

	playStyle   = lipgloss.NewStyle().Foreground(accent).Bold(true)
	selStyle    = lipgloss.NewStyle().Foreground(cream).Bold(true)
	titleStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("250"))
	artistStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	durStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))

	badgeStyle  = lipgloss.NewStyle().Foreground(cream).Background(accent).Bold(true).Padding(0, 0)
	badgeDim    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	promptStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(accentSoft).
			Padding(1, 2)
)

// --- content width -------------------------------------------------------

func (m Model) contentWidth() int {
	w := m.width - 4
	if w > 80 {
		w = 80
	}
	if w < 20 {
		w = 20
	}
	return w
}

// --- custom list delegate ------------------------------------------------

type delegate struct{}

func (delegate) Height() int                             { return 1 }
func (delegate) Spacing() int                            { return 0 }
func (delegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd { return nil }

// Render draws one queue row: [▶] title · artist ... duration
func (delegate) Render(w io.Writer, l list.Model, index int, li list.Item) {
	t, ok := li.(trackItem)
	if !ok {
		return
	}

	inner := l.Width() - 8 // borders, padding, cursor
	textW := inner - 8     // leave room for duration

	row := "  "
	name := truncate(t.title, textW)
	if t.playing {
		row = playStyle.Render("▶ ") + selStyle.Render(name)
	} else {
		row = "  " + titleStyle.Render(name)
	}
	if art := t.artist; art != "" {
		if rem := textW - len([]rune(t.title)) - 3; rem > 4 {
			row += artistStyle.Render(" · " + truncate(art, rem))
		}
	}
	row += durStyle.Render(fmt.Sprintf("%7s", t.dur))

	fmt.Fprint(w, row)
}

// --- layout --------------------------------------------------------------

func (m *Model) layout() {
	w := m.contentWidth()
	listW := w - 4 // border + padding each side

	h := m.height - 9
	if h < 3 {
		h = 3
	}
	m.list.SetSize(listW, h)

	barW := listW - 14
	if barW < 10 {
		barW = 10
	}
	m.prog.Width = barW
}

// --- view ----------------------------------------------------------------

func (m Model) View() string {
	if m.width == 0 {
		return strings.Repeat("\n", 12) + dimStyle.Render(strings.Repeat(" ", 30)+"starting…")
	}
	if m.inputOn {
		return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.inputView())
	}
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, m.mainView())
}

func (m Model) inputView() string {
	head := gradient("♪ ytmg", "#7f1d1d", "#ff4d4d")

	body := headStyle.Render(head) +
		dimStyle.Render(" · youtube music tui") + "\n\n" +
		m.input.View() + "\n\n" +
		dimStyle.Render("enter load · esc back · ctrl+c quit")
	if m.toast.msg != "" {
		st := dimStyle
		if m.toast.bad {
			st = lipgloss.NewStyle().Foreground(accentSoft)
		}
		body += "\n\n" + st.Render(truncate(m.toast.msg, 60))
	}
	return promptStyle.Render(body)
}

func (m Model) mainView() string {
	w := m.contentWidth()

	content := lipgloss.JoinVertical(lipgloss.Center,
		m.header(w),
		"",
		m.nowPlaying(w),
		boxStyle.Render(m.list.View()),
		m.progressRow(w),
		m.statusLine(w),
		"",
		m.helpLine(w),
	)
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, content)
}

func (m Model) header(w int) string {
	title := headStyle.Render(gradient("♪ ytmg", "#7f1d1d", "#ff4d4d"))
	sub := dimStyle.Render(" · youtube music tui")

	var badges []string
	if m.repeatOne {
		badges = append(badges, badgeStyle.Render("repeat-one"))
	}
	if m.repeatAll {
		badges = append(badges, badgeStyle.Render("repeat-all"))
	}
	if m.shuffle {
		badges = append(badges, badgeStyle.Render("shuffle"))
	}
	mid := ""
	if len(badges) > 0 {
		mid = "  " + strings.Join(badges, " ")
	}

	left := title + sub + mid
	right := badgeDim.Render(fmt.Sprintf("♪ volume:%d", m.volume))

	leftW := lipgloss.Width(left)
	gap := w - leftW - lipgloss.Width(right)
	if gap > 0 {
		left += strings.Repeat(" ", gap)
	}
	return left + right
}

func (m Model) nowPlaying(w int) string {
	switch {
	case m.fetching:
		return dimStyle.Render("· loading playlist…")
	case len(m.tracks) == 0:
		return dimStyle.Render("· no tracks — press o to open a playlist URL")
	case m.ended:
		return dimStyle.Render("· queue finished — n to wrap around")
	default:
		t := m.tracks[m.cur]
		label := t.Title
		if artist := t.Artist(); artist != "" {
			label = t.Title + " — " + artist
		}
		return playStyle.Render("▶ ") + titleStyle.Render(truncate(label, w-4))
	}
}

// progressRow renders "0:42  ▮▮▮██▯  3:11"
func (m Model) progressRow(w int) string {
	var pos, dur string
	if m.p != nil {
		st := m.p.Status()
		pos, dur = yt.Format(st.TimePos), yt.Format(st.Duration)
	}
	if pos == "" {
		pos = "--:--"
	}
	if dur == "" {
		dur = "--:--"
	}

	bar := m.prog.ViewAs(m.percent())
	row := dimStyle.Render(pos) + " " + bar + " " + dimStyle.Render(dur)
	return truncate(row, w) // keep the row inside the column
}

func (m Model) statusLine(w int) string {
	if m.toast.msg != "" {
		st := dimStyle
		if m.toast.bad {
			st = lipgloss.NewStyle().Foreground(accentSoft)
		}
		return st.Render(truncate(m.toast.msg, w))
	}
	// idle usage line
	label := "· " + truncate(m.plTitle, w-45) + " ·"
	if m.plTitle == "" {
		label = ""
	}
	return dimStyle.Render(label)
}

func (m Model) helpLine(w int) string {
	help := "q quit · space pause · n next p prev · F repeat-one · R repeat-all · S shuffle · ← → seek ±10s · ± vol · o url"
	return dimStyle.Render(truncate(help, w))
}

func (m Model) percent() float64 {
	if m.p == nil || !m.started {
		return 0
	}
	st := m.p.Status()
	if st.Duration <= 0 {
		return 0
	}
	p := st.TimePos / st.Duration
	if p < 0 {
		p = 0
	}
	if p > 1 {
		p = 1
	}
	return p
}

// --- helpers -------------------------------------------------------------

// truncate cuts a string to at most w runes, marking cuts with an ellipsis.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return string(r[:w-1]) + "…"
}

// gradient colors each rune between two hex colors.
func gradient(s, from, to string) string {
	fr, okF := hexRGB(from)
	to3, okT := hexRGB(to)
	if !okF || !okT {
		return s
	}
	r := []rune(s)
	var b strings.Builder
	for i, c := range r {
		t := 0.0
		if len(r) > 1 {
			t = float64(i) / float64(len(r)-1)
		}
		var rgb [3]int
		for j := range rgb {
			rgb[j] = int(float64(fr[j]) + t*float64(to3[j]-fr[j]))
		}
		fmt.Fprintf(&b, "\x1b[38;2;%d;%d;%dm%c\x1b[0m", rgb[0], rgb[1], rgb[2], c)
	}
	return b.String()
}

func hexRGB(h string) ([3]int, bool) {
	h = strings.TrimPrefix(h, "#")
	if len(h) != 6 {
		return [3]int{}, false
	}
	var out [3]int
	for i := range out {
		if _, err := fmt.Sscanf(h[i*2:i*2+2], "%02x", &out[i]); err != nil {
			return [3]int{}, false
		}
	}
	return out, true
}
