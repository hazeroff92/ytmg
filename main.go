// ytmg — a small YouTube Music TUI player: bubbletea + yt-dlp + mpv IPC.
package main

import (
	"flag"
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"

	"ytmg/internal/app"
	"ytmg/internal/config"
)

func main() {
	cookies := flag.String("cookies", "", "path to cookies.txt in Netscape format")
	flag.Parse()

	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		os.Exit(1)
	}

	m := app.New(cfg, *cookies)
	if _, err := tea.NewProgram(m, tea.WithAltScreen()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
