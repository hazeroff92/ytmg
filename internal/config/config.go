// Package config loads and saves the small TOML config file.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	toml "github.com/BurntSushi/toml"
)

// Config mirrors the on-disk config.toml.
type Config struct {
	Playlist string `toml:"playlist"` // YouTube Music playlist URL
	Cookies  string `toml:"cookies"`  // optional path to cookies.txt (Netscape format)
	Volume   int    `toml:"volume"`
}

// Dir returns the config directory, e.g. ~/.config/ytmg.
func Dir() string {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "ytmg")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".config", "ytmg")
	}
	return ".ytmg"
}

// Path is the config file path.
func Path() string { return filepath.Join(Dir(), "config.toml") }

// Default returns sane defaults.
func Default() Config { return Config{Volume: 85} }

// Load reads the config. A missing file is not an error.
func Load() (Config, error) {
	c := Default()
	data, err := os.ReadFile(Path())
	switch {
	case os.IsNotExist(err):
		return c, nil
	case err != nil:
		return c, err
	}
	if err := toml.Unmarshal(data, &c); err != nil {
		return c, fmt.Errorf("parse %s: %w", Path(), err)
	}
	return c, nil
}

// Save writes the config back to disk.
func Save(c Config) error {
	if err := os.MkdirAll(Dir(), 0o755); err != nil {
		return err
	}
	f, err := os.Create(Path())
	if err != nil {
		return err
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(c)
}

// ResolveCookies picks the cookies file: CLI flag beats config beats the
// conventional ~/.config/ytmg/cookies.txt if it exists.
func ResolveCookies(c Config, cliPath string) string {
	if cliPath != "" {
		return cliPath
	}
	if c.Cookies != "" {
		return c.Cookies
	}
	p := filepath.Join(Dir(), "cookies.txt")
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		return p
	}
	return ""
}
