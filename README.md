# ytmg

A minimal YouTube Music player for the terminal: **Go + bubbletea + yt-dlp + mpv (IPC)**.

- Loads a playlist/album URL as JSON via `yt-dlp --flat-playlist -J`
- Plays every track through **mpv** over a JSON IPC **unix socket** (audio only)
- Supports **cookies.txt in Netscape format** (for private / age-gated playlists)
- Centered queue, gradient header, animated progress bar, playing-row `▶`

## Requirements

| Tool    | Why                          |
|---------|------------------------------|
| `mpv`   | playback (resolves YouTube itself via yt-dlp) |
| `yt-dlp`| playlist discovery           |
| `go`    | build                        |

## Build & run

```bash
go build -o ytmg .
./ytmg                       # uses + saves config on first URL entry
./ytmg --cookies /path/to/cookies.txt
```

## Config — `~/.config/ytmg/config.toml`

```toml
playlist = "https://music.youtube.com/playlist?list=..."
cookies  = ""            # or an explicit cookies.txt path; empty = auto ~/.config/ytmg/cookies.txt
volume   = 85
```

`--cookies` (CLI) beats `cookies` (config) beats `~/.config/ytmg/cookies.txt`.

## Keys

| Key            | Action                    |
|----------------|---------------------------|
| `space`        | pause / play              |
| `n`/`j` / `p`/`k` | next / prev (`p >3s` restarts) |
| `S` on `next` | random unplayed track |
| `S` on `prev` | return to previously played (history stack) |
| `F`            | repeat one (single track loop) |
| `R`            | repeat all (queue loop)   |
| `S`            | shuffle mode (random next, no consecutive repeats) |
| `←` / `→`      | seek ±10 s                |
| `+` / `-`      | volume ±5                 |
| `o`            | open another playlist URL (saved to config) |
| ↑/↓            | move in queue             |
| `q`            | quit (kills mpv)          |

EOF/no-repeat ends the queue; `n` always wraps manually.

## Layout

```
main.go                 flags + config load
internal/config/        TOML config load/save
internal/yt/            yt-dlp wrapper (flat-playlist JSON, dedup, filters)
internal/mpv/           mpv JSON-IPC client over unix socket
internal/app/           bubbletea model + lipgloss view
```

Design intentionally KISS/YAGNI: one config file, one socket, three playback modes.
