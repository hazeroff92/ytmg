// Package app holds the bubbletea model, update loop and wiring.
package app

import (
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"ytmg/internal/config"
	"ytmg/internal/mpv"
	"ytmg/internal/yt"
)

// trackItem is one row in the queue list.
type trackItem struct {
	index   int
	title   string
	artist  string
	dur     string
	playing bool
}

func (t trackItem) Title() string       { return t.title }
func (t trackItem) Description() string { return t.artist }
func (t trackItem) FilterValue() string { return t.title + " " + t.artist }

type toast struct {
	msg string
	bad bool
	id  int
}

// Model is the whole application state.
type Model struct {
	cfg        config.Config
	cliCookies string

	tracks   []yt.Track
	plTitle  string
	cur      int
	ended    bool
	started  bool
	fetching bool

	p *mpv.Player

	repeatOne  bool
	repeatAll  bool
	shuffle    bool
	visited    map[int]bool
	history    []int // для prev в режиме shuffle
	failStreak int   // подряд неудачных треков (для auto-skip)

	volume int

	list    list.Model
	prog    progress.Model
	input   textinput.Model
	inputOn bool

	toast         toast
	width, height int
}

// New builds the initial model.
func New(cfg config.Config, cliCookies string) Model {
	c := cfg
	if c.Volume == 0 {
		c.Volume = 85
	}
	m := Model{
		cfg:        c,
		cliCookies: cliCookies,
		visited:    map[int]bool{},
		volume:     c.Volume,
		history:    []int{},
	}
	m.list = list.New(nil, delegate{}, 60, 10)
	m.list.SetShowStatusBar(false)
	m.list.SetShowHelp(false)
	m.list.SetFilteringEnabled(false)
	m.list.Title = "Queue · 0"

	m.prog = progress.New(progress.WithScaledGradient("#7f1d1d", "#ffb3c1"))
	m.prog.Width = 40

	if m.cfg.Playlist == "" {
		m.input = newInput()
		m.inputOn = true
		m.input.Focus() // Init runs on a value copy, so focus here
	} else {
		m.fetching = true
	}
	// если пользователь явно указал cookies, но файла нет — предупредим сразу
	if explicit := m.cfg.Cookies; explicit != "" || cliCookies != "" {
		if config.ResolveCookies(c, cliCookies) == "" {
			m.toast = toast{msg: "cookies file not found — playing without it", bad: true, id: 1}
		}
	}
	return m
}

func newInput() textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "paste a YouTube Music playlist URL"
	ti.Prompt = "> "
	ti.Width = 64
	return ti
}

type fetchMsg struct {
	title  string
	tracks []yt.Track
	err    error
}

type mpvMsg struct {
	p   *mpv.Player
	err error
}

type evMsg struct {
	kind mpv.Kind
	err  string
}

type mpvDeadMsg struct{}

type tickMsg struct{}

type toastMsg struct{ id int }

type skipMsg struct{}

func (m Model) cookies() string { return config.ResolveCookies(m.cfg, m.cliCookies) }

func (m Model) Init() tea.Cmd {
	cmds := []tea.Cmd{
		startMPVCmd(m.cookies(), m.volume),
		tick(),
	}
	if m.toast.msg != "" {
		id := m.toast.id
		cmds = append(cmds, tea.Tick(4*time.Second, func(time.Time) tea.Msg {
			return toastMsg{id: id}
		}))
	}
	if m.inputOn {
		cmds = append(cmds, textinput.Blink)
	} else {
		cmds = append(cmds, fetchCmd(m.cfg.Playlist, m.cookies()))
	}
	return tea.Batch(cmds...)
}

func fetchCmd(url, cookies string) tea.Cmd {
	return func() tea.Msg {
		title, tracks, err := yt.Fetch(url, cookies)
		return fetchMsg{title: title, tracks: tracks, err: err}
	}
}

func startMPVCmd(cookies string, volume int) tea.Cmd {
	return func() tea.Msg {
		p, err := mpv.Start(cookies, volume)
		return mpvMsg{p: p, err: err}
	}
}

func waitEvent(p *mpv.Player) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-p.Events
		if !ok {
			return mpvDeadMsg{}
		}
		return evMsg{kind: ev.Kind, err: ev.Err}
	}
}

func tick() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg { return tickMsg{} })
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			return m, m.quitCmd()
		}
		if m.inputOn {
			return m.updateInput(msg)
		}
		return m.updateKeys(msg)

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case fetchMsg:
		m.fetching = false
		if msg.err != nil {
			m.notify("playlist: "+msg.err.Error(), true)
			return m, nil
		}
		m.plTitle, m.tracks = msg.title, msg.tracks
		m.visited = map[int]bool{}
		m.refreshItems()
		if len(m.tracks) == 0 {
			m.notify("playlist is empty", true)
			return m, nil
		}
		return m, m.tryStart()

	case mpvMsg:
		if msg.err != nil {
			m.notify("mpv: "+msg.err.Error(), true)
			return m, nil
		}
		m.p = msg.p
		return m, tea.Batch(waitEvent(m.p), m.tryStart())

	case mpvDeadMsg:
		m.p = nil
		m.notify("mpv exited", true)
		return m, nil

	case evMsg:
		switch msg.kind {
		case mpv.EOF:
			m.failStreak = 0
			return m, m.autoAdvance()
		case mpv.ErrorKind:
			m.failStreak++
			m.notify("playback: "+msg.err, true)
			if m.failStreak >= len(m.tracks) {
				m.notify(fmt.Sprintf("stopping: %d tracks failed in a row", m.failStreak), true)
				m.failStreak = 0
				return m, tick()
			}
			return m, tea.Tick(1200*time.Millisecond, func(time.Time) tea.Msg { return skipMsg{} })
		}

	case skipMsg:
		if m.p == nil || len(m.tracks) == 0 { //nolint:errcheck
			return m, nil
		}
		return m, m.loadTrack(m.nextPlaybackIndex(), true)

	case tickMsg:
		return m, tea.Batch(tick(), m.syncProgress())

	case progress.FrameMsg:
		p, cmd := m.prog.Update(msg)
		m.prog = p.(progress.Model)
		return m, cmd

	case toastMsg:
		if msg.id == m.toast.id {
			m.toast.msg = ""
		}
		return m, nil
	}
	return m, nil
}

func (m *Model) updateInput(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "esc":
		m.inputOn = false
		return m, nil
	case "enter":
		url := strings.TrimSpace(m.input.Value())
		if url == "" || (!strings.Contains(url, "music.youtube.com") &&
			!strings.Contains(url, "youtube.com") && !strings.Contains(url, "youtu.be") &&
			!strings.HasPrefix(url, "ytsearch")) {
			m.notify("not a youtube url", true)
			return m, nil
		}
		m.cfg.Playlist = url
		if err := config.Save(m.cfg); err != nil {
			m.notify("save config: "+err.Error(), true)
		} else {
			m.notify("saved to "+m.cfg.Playlist, false)
		}
		m.inputOn = false
		m.fetching = true
		m.started = false
		m.tracks = nil
		m.visited = map[int]bool{}
		m.ended = false
		m.prog.SetPercent(0)
		m.refreshItems()
		return m, fetchCmd(url, m.cookies())
	}
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	return m, cmd
}

func (m *Model) updateKeys(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch k.String() {
	case "q":
		return m, m.quitCmd()

	case " ":
		if m.p == nil || !m.started {
			return m, nil
		}
		return m, m.bgCall(func() error { return m.p.TogglePause() })

	case "n", "j":
		return m, m.manualNext()
	case "p", "k":
		return m, m.manualPrev()

	case "F":
		m.repeatOne = !m.repeatOne
		if m.repeatOne {
			m.repeatAll = false
		}
		on := m.repeatOne
		m.notify(map[bool]string{true: "repeat one on", false: "repeat one off"}[on], false)
		return m, m.bgCall(func() error { return m.p.SetRepeatOne(on) })

	case "R":
		m.repeatAll = !m.repeatAll
		if m.repeatAll && m.repeatOne {
			m.repeatOne = false
			off := false
			return m, tea.Batch(
				m.bgCall(func() error { return m.p.SetRepeatOne(off) }),
				m.notify("repeat all on", false),
			)
		}
		m.notify(map[bool]string{true: "repeat all on", false: "repeat all off"}[m.repeatAll], false)
		return m, nil

	case "S":
		m.shuffle = !m.shuffle
		m.notify(map[bool]string{true: "shuffle on", false: "shuffle off"}[m.shuffle], false)
		return m, nil

	case "+", "=":
		return m, m.vol(+5)
	case "-", "_":
		return m, m.vol(-5)

	case "left":
		return m, m.doSeek(-10, "« 10s")
	case "right":
		return m, m.doSeek(10, "10s »")

	case "o":
		m.input = newInput()
		m.input.SetValue(m.cfg.Playlist)
		m.input.Focus()
		m.inputOn = true
		return m, textinput.Blink
	}

	var cmd tea.Cmd
	m.list, cmd = m.list.Update(k)
	return m, cmd
}

func (m *Model) vol(d int) tea.Cmd {
	if m.p == nil {
		return nil
	}
	m.volume = m.volume + d
	v := m.volume
	return tea.Batch(
		m.bgCall(func() error { return m.p.SetVolume(v) }),
		m.notify(fmt.Sprintf("volume %d", m.volume), false),
	)
}

func (m *Model) doSeek(d float64, label string) tea.Cmd {
	if m.p == nil || !m.started {
		return nil
	}
	return tea.Batch(
		m.bgCall(func() error { return m.p.Seek(d) }),
		m.notify(label, false),
	)
}

// bgCall runs a player call on the command goroutine and surfaces errors as toasts.
func (m *Model) bgCall(f func() error) tea.Cmd {
	return func() tea.Msg {
		if err := f(); err != nil {
			return evMsg{kind: mpv.ErrorKind, err: err.Error()}
		}
		return nil
	}
}

func (m *Model) tryStart() tea.Cmd {
	if m.started || m.p == nil || len(m.tracks) == 0 {
		return nil
	}
	m.started = true
	return m.loadTrack(0, false)
}

// loadTrack starts track i; push=true records the current track in history.
func (m *Model) loadTrack(i int, push bool) tea.Cmd {
	if m.p == nil || len(m.tracks) == 0 {
		return nil
	}
	if push {
		m.history = append(m.history, m.cur)
	}
	m.cur = i
	m.ended = false
	if m.visited == nil {
		m.visited = map[int]bool{}
	}
	m.visited[i] = true

	url := m.tracks[i].WatchURL()
	m.prog.SetPercent(0)
	m.refreshItems()

	t := m.tracks[i]
	return tea.Batch(
		m.bgCall(func() error { return m.p.Load(url) }),
		m.notify(fmt.Sprintf("%d/%d · %s", i+1, len(m.tracks), t.Title), false),
	)
}

// autoAdvance reacts to a naturally-finished track.
func (m *Model) autoAdvance() tea.Cmd {
	if m.p == nil || len(m.tracks) == 0 {
		return tick()
	}
	if m.repeatOne {
		return tick() // mpv loops natively; stray EOF is ignored
	}

	if m.shuffle {
		if i, ok := m.pickRandomNext(); ok {
			return m.loadTrack(i, true)
		}
		if m.repeatAll {
			cur := m.cur
			m.visited = map[int]bool{cur: true}
			if i, ok := m.pickRandomNext(); ok {
				return m.loadTrack(i, true)
			}
		}
		return m.finishQueue()
	}

	if next := m.cur + 1; next < len(m.tracks) {
		return m.loadTrack(next, true)
	}
	if m.repeatAll {
		return m.loadTrack(0, true)
	}
	return m.finishQueue()
}

func (m *Model) pickRandomNext() (int, bool) {
	if len(m.tracks) <= 1 {
		return 0, m.repeatAll && len(m.tracks) == 1
	}
	cands := make([]int, 0, len(m.tracks))
	for i := range m.tracks {
		if i != m.cur && !m.visited[i] {
			cands = append(cands, i)
		}
	}
	if len(cands) == 0 {
		return 0, false
	}
	return cands[rand.Intn(len(cands))], true
}

// nextPlaybackIndex is where the queue should jump if the current track dies.
func (m *Model) nextPlaybackIndex() int {
	if m.shuffle {
		if i, ok := m.pickRandomNext(); ok {
			return i
		}
		m.visited = map[int]bool{m.cur: true}
		if i, ok := m.pickRandomNext(); ok {
			return i
		}
	}
	return (m.cur + 1) % len(m.tracks)
}

func (m *Model) finishQueue() tea.Cmd {
	m.ended = true
	m.refreshItems()
	m.notify("queue finished", false)
	return tick()
}

func (m *Model) manualNext() tea.Cmd {
	if m.p == nil || len(m.tracks) == 0 {
		return nil
	}
	m.failStreak = 0
	if m.shuffle {
		// случайный следующий — сначала из непроигранных
		if i, ok := m.pickRandomNext(); ok {
			return m.loadTrack(i, true)
		}
		// всё проиграно: пользовательская клавиша — начинаем новый круг
		m.visited = map[int]bool{}
		m.visited[m.cur] = true
		if len(m.tracks) == 1 {
			return m.loadTrack(0, false)
		}
		if i, ok := m.pickRandomNext(); ok {
			return m.loadTrack(i, true)
		}
		return m.loadTrack((m.cur+1)%len(m.tracks), true)
	}
	return m.loadTrack((m.cur+1)%len(m.tracks), true)
}

func (m *Model) manualPrev() tea.Cmd {
	if m.p == nil || len(m.tracks) == 0 {
		return nil
	}
	m.failStreak = 0
	if m.shuffle {
		// вернуться к прошлому проигранному по истории
		if n := len(m.history); n > 0 {
			i := m.history[n-1]
			m.history = m.history[:n-1]
			return m.loadTrack(i, false)
		}
		// история пуста — предыдущий по порядку, не приближая конец круга
		return m.loadTrack((m.cur-1+len(m.tracks))%len(m.tracks), false)
	}
	if st := m.p.Status(); st.TimePos > 3.0 {
		return m.bgCall(func() error { return m.p.Restart() })
	}
	return m.loadTrack((m.cur-1+len(m.tracks))%len(m.tracks), true)
}

// syncProgress pushes the progress bar forward (and keeps ticking).
func (m *Model) syncProgress() tea.Cmd {
	if m.p == nil || !m.started {
		return nil
	}
	st := m.p.Status()
	if st.Duration <= 0 {
		return nil
	}
	pct := st.TimePos / st.Duration
	if pct < 0 {
		pct = 0
	}
	if pct > 1 {
		pct = 1
	}
	return m.prog.SetPercent(pct)
}

// notify sets the toast and schedules its cleanup.
func (m *Model) notify(msg string, bad bool) tea.Cmd {
	m.toast.id++
	m.toast.msg, m.toast.bad = msg, bad
	id := m.toast.id
	return tea.Tick(2500*time.Millisecond, func(time.Time) tea.Msg {
		return toastMsg{id: id}
	})
}

func (m *Model) quitCmd() tea.Cmd {
	if m.p == nil {
		return tea.Quit
	}
	p := m.p
	return tea.Batch(tea.Quit, func() tea.Msg {
		p.Quit()
		return nil
	})
}

func (m *Model) refreshItems() {
	items := make([]list.Item, len(m.tracks))
	for i, t := range m.tracks {
		dur := yt.Format(t.Duration)
		if dur == "" {
			dur = "--:--"
		}
		items[i] = trackItem{
			index:   i,
			title:   t.Title,
			artist:  t.Artist(),
			dur:     dur,
			playing: i == m.cur && !m.ended,
		}
	}
	if err := m.list.SetItems(items); err != nil { //nolint:errcheck
	}
	m.list.Title = fmt.Sprintf("Queue · %d", len(m.tracks))
}
