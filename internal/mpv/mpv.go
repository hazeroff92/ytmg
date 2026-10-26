// Package mpv talks to an mpv process over its JSON IPC unix socket.
package mpv

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// Status is the current playback snapshot.
type Status struct {
	TimePos  float64
	Duration float64
	Paused   bool
}

// Kind distinguishes event types pushed on the Events channel.
type Kind int

const (
	EOF Kind = iota // track ended
	ErrorKind
)

// Event is what the UI reacts to.
type Event struct {
	Kind Kind
	Err  string
}

type resp struct {
	Error     string `json:"error"`
	RequestID int    `json:"request_id"`
	Data      any    `json:"data"`
}

type frame struct {
	Event      string          `json:"event"`
	Reason     string          `json:"reason"`
	FileError  string          `json:"file_error"`
	PropertyID int             `json:"id"`
	Data       json.RawMessage `json:"data"`

	// response fields
	Error     string `json:"error"`
	RequestID int    `json:"request_id"`
}

// Player owns the mpv process and its IPC connection.
type Player struct {
	proc *exec.Cmd
	conn net.Conn

	writeMu sync.Mutex
	writes  chan []byte
	pendMu  sync.Mutex
	pend    map[int]chan resp
	reqID   int

	Events chan Event

	mu sync.RWMutex
	st Status

	quitOnce sync.Once
	dead     chan struct{}
}

// SocketPath is where we create the IPC socket.
func SocketPath() string {
	dir := os.Getenv("XDG_RUNTIME_DIR")
	if dir == "" {
		dir = os.TempDir()
	}
	return filepath.Join(dir, fmt.Sprintf("ytmg-%d.sock", os.Getuid()))
}

// Start boots mpv headless and connects to its IPC socket.
func Start(cookiesFile string, volume int) (*Player, error) {
	sock := SocketPath()
	os.Remove(sock)

	args := []string{
		"--no-video",
		"--no-terminal",
		"--no-audio-display",
		"--really-quiet",
		"--idle=yes",
		"--input-ipc-server=" + sock,
		fmt.Sprintf("--volume=%d", clamp(volume, 0, 100)),
	}
	if cookiesFile != "" {
		args = append(args, "--ytdl-raw-options=cookies-file="+cookiesFile)
	}

	proc := exec.Command("mpv", args...)
	if err := proc.Start(); err != nil {
		return nil, fmt.Errorf("start mpv: %w", err)
	}

	conn, err := dialUnix(sock, 5*time.Second)
	if err != nil {
		proc.Process.Kill() //nolint:errcheck
		return nil, err
	}

	p := &Player{
		proc:   proc,
		conn:   conn,
		writes: make(chan []byte, 64),
		pend:   make(map[int]chan resp),
		Events: make(chan Event, 64),
		dead:   make(chan struct{}),
	}

	go p.writeLoop()
	go p.readLoop()

	// observe properties we care about: 1 pause, 2 time-pos, 3 duration
	for id, prop := range map[int]string{1: "pause", 2: "time-pos", 3: "duration"} {
		if err := p.call("observe_property", id, prop); err != nil {
			p.Quit()
			return nil, fmt.Errorf("observe %s: %w", prop, err)
		}
	}
	return p, nil
}

func dialUnix(path string, d time.Duration) (net.Conn, error) {
	deadline := time.Now().Add(d)
	for {
		c, err := net.Dial("unix", path)
		if err == nil {
			return c, nil
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("waiting for mpv socket: %w", err)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// call sends a JSON IPC command and waits for its response.
func (p *Player) call(args ...any) error {
	p.pendMu.Lock()
	p.reqID++
	id := p.reqID
	ch := make(chan resp, 1)
	p.pend[id] = ch
	p.pendMu.Unlock()

	payload := map[string]any{"command": args, "request_id": id}
	b, _ := json.Marshal(payload)
	b = append(b, '\n')

	select {
	case p.writes <- b:
	case <-p.dead:
		return errors.New("mpv died")
	default:
		p.pendMu.Lock()
		delete(p.pend, id)
		p.pendMu.Unlock()
		return errors.New("mpv: write queue full")
	}

	select {
	case r := <-ch:
		if r.Error != "success" {
			return fmt.Errorf("mpv: %s", r.Error)
		}
		return nil
	case <-time.After(8 * time.Second):
		p.pendMu.Lock()
		delete(p.pend, id)
		p.pendMu.Unlock()
		return errors.New("mpv: timeout")
	}
}

func (p *Player) writeLoop() {
	for {
		select {
		case b := <-p.writes:
			if err := p.writeLine(b); err != nil {
				return
			}
		case <-p.dead:
			return
		}
	}
}

func (p *Player) writeLine(b []byte) error {
	p.writeMu.Lock()
	defer p.writeMu.Unlock()
	_, err := p.conn.Write(b)
	return err
}

func (p *Player) readLoop() {
	defer close(p.dead)
	sc := bufio.NewScanner(p.conn)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		p.dispatch(sc.Bytes())
	}
}

// dispatch routes one IPC message: either a command response or an event.
func (p *Player) dispatch(line []byte) {
	var m map[string]json.RawMessage
	if json.Unmarshal(line, &m) != nil {
		return
	}

	// response
	if ridRaw, ok := m["request_id"]; ok {
		var r resp
		if json.Unmarshal(line, &r) != nil {
			return
		}
		var id int
		json.Unmarshal(ridRaw, &id) //nolint:errcheck
		p.pendMu.Lock()
		ch := p.pend[id]
		delete(p.pend, id)
		p.pendMu.Unlock()
		if ch != nil {
			ch <- r
		}
		return
	}

	var ev string
	if raw, ok := m["event"]; ok {
		json.Unmarshal(raw, &ev) //nolint:errcheck
	} else {
		return
	}

	switch ev {
	case "property-change":
		var id int
		json.Unmarshal(m["id"], &id) //nolint:errcheck
		switch id {
		case 1:
			var b bool
			json.Unmarshal(m["data"], &b) //nolint:errcheck
			p.mu.Lock()
			p.st.Paused = b
			p.mu.Unlock()
		case 2:
			var f float64
			json.Unmarshal(m["data"], &f) //nolint:errcheck
			p.mu.Lock()
			p.st.TimePos = f
			p.mu.Unlock()
		case 3:
			var d float64
			json.Unmarshal(m["data"], &d) //nolint:errcheck
			p.mu.Lock()
			p.st.Duration = d
			p.mu.Unlock()
		}

	case "end-file":
		var reason string
		json.Unmarshal(m["reason"], &reason) //nolint:errcheck
		switch reason {
		case "eof":
			p.push(EOF, "")
		case "error":
			msg := "track error"
			if raw, ok := m["file_error"]; ok {
				var fe string
				if json.Unmarshal(raw, &fe) == nil && fe != "" {
					msg = fe
				}
			}
			p.push(ErrorKind, msg)
		}
	}
}

// push delivers an event to the UI without ever blocking mpv's reader.
func (p *Player) push(k Kind, errMsg string) {
	e := Event{Kind: k, Err: errMsg}
	for {
		select {
		case p.Events <- e:
			return
		default:
			// drain oldest to make room
			select {
			case <-p.Events:
			default:
			}
			select {
			case p.Events <- e:
				return
			default:
				return
			}
		}
	}
}

// Status snapshots current playback state.
func (p *Player) Status() Status {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.st
}

// Load plays a URL, replacing the current file.
func (p *Player) Load(url string) error {
	p.mu.Lock()
	p.st.TimePos = 0
	p.st.Duration = 0
	p.mu.Unlock()
	return p.call("loadfile", url, "replace")
}

// TogglePause cycles pause.
func (p *Player) TogglePause() error { return p.call("cycle", "pause") }

// SetVolume sets the playback volume (0-100).
func (p *Player) SetVolume(v int) error { return p.call("set", "volume", float64(clamp(v, 0, 100))) }

// Seek moves relative to the current position, in seconds.
func (p *Player) Seek(delta float64) error {
	if p.Status().Duration <= 0 {
		return nil // nothing loaded yet
	}
	return p.call("seek", delta, "relative")
}

// Restart restarts the current track.
func (p *Player) Restart() error {
	if p.Status().Duration <= 0 {
		return nil
	}
	return p.call("seek", 0, "absolute")
}

// SetRepeatOne toggles native single-track looping.
func (p *Player) SetRepeatOne(on bool) error {
	val := "no"
	if on {
		val = "inf"
	}
	return p.call("set", "loop-file", val)
}

// Stop pauses the player (used between playlist switches).
func (p *Player) Stop() error {
	return p.call("set", "pause", true)
}

// Quit stops mpv and cleans up.
func (p *Player) Quit() {
	p.quitOnce.Do(func() {
		p.writeLine([]byte("{\"command\":[\"quit\"]}\n"))
		// make sure the process does not linger
		if p.proc != nil {
			done := make(chan struct{})
			go func() { p.proc.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(2 * time.Second):
				p.proc.Process.Kill() //nolint:errcheck
				<-done
			}
		}
		p.conn.Close()
	})
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
