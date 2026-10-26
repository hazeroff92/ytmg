// Package yt wraps yt-dlp for playlist discovery.
package yt

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// Track is one entry of a playlist.
type Track struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	Uploader string  `json:"uploader"`
	Channel  string  `json:"channel"`
	URL      string  `json:"url"`
	Duration float64 `json:"duration"`
}

type playlist struct {
	Title   string  `json:"title"`
	Entries []Track `json:"entries"`
}

// Artist returns the best known channel name.
func (t Track) Artist() string {
	if t.Uploader != "" {
		return t.Uploader
	}
	return t.Channel
}

// WatchURL builds the playable URL for mpv.
func (t Track) WatchURL() string {
	if t.ID != "" {
		return "https://music.youtube.com/watch?v=" + t.ID
	}
	return t.URL
}

// Fetch reads a playlist as JSON via `yt-dlp --flat-playlist -J`.
// cookies may be empty; the first argument is the playlist title.
func Fetch(url, cookies string) (string, []Track, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	args := []string{"--flat-playlist", "-J", "--no-warnings", url}
	if cookies != "" {
		args = append(args, "--cookies", cookies)
	}
	cmd := exec.CommandContext(ctx, "yt-dlp", args...)

	var errOut strings.Builder
	cmd.Stderr = &errOut
	out, err := cmd.Output()
	if err != nil {
		msg := tail(errOut.String(), 3)
		if msg != "" {
			return "", nil, fmt.Errorf("yt-dlp: %s", msg)
		}
		return "", nil, fmt.Errorf("yt-dlp: %w", err)
	}

	var p playlist
	if err := json.Unmarshal(out, &p); err != nil {
		return "", nil, fmt.Errorf("parse yt-dlp output: %w", err)
	}

	seen := map[string]bool{}
	tracks := make([]Track, 0, len(p.Entries))
	for _, e := range p.Entries {
		if e.ID == "" && e.URL == "" {
			continue
		}
		if strings.HasPrefix(e.Title, "[Private") ||
			strings.HasPrefix(e.Title, "[Unavailable") ||
			strings.HasPrefix(e.Title, "[Deleted") {
			continue
		}
		if seen[e.ID] {
			continue // YouTube Music playlists often contain duplicates
		}
		seen[e.ID] = true
		tracks = append(tracks, e)
	}
	return p.Title, tracks, nil
}

// Format renders seconds as m:ss ("" when unknown).
func Format(d float64) string {
	if d <= 0 {
		return ""
	}
	t := int(d + 0.5)
	return fmt.Sprintf("%d:%02d", t/60, t%60)
}

func tail(s string, lines int) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	parts := strings.Split(s, "\n")
	if len(parts) > lines {
		parts = parts[len(parts)-lines:]
	}
	return strings.Join(parts, "; ")
}
