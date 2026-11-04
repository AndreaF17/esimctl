// Package theme reads the colors of the active Omarchy theme.
package theme

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Colors maps colors.toml keys (accent, foreground, red, ...) to hex values.
type Colors map[string]string

var defaults = Colors{
	"accent": "#7aa2f7", "foreground": "#c0caf5", "dark_foreground": "#565f89",
	"background": "#1a1b26", "selection": "#283457",
	"red": "#f7768e", "green": "#9ece6a", "yellow": "#e0af68", "cyan": "#7dcfff",
}

// Path returns the active theme's colors.toml.
func Path() string {
	if s := os.Getenv("XDG_STATE_HOME"); s != "" {
		return filepath.Join(s, "omarchy/current/theme/colors.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local/state/omarchy/current/theme/colors.toml")
}

// Load reads the active theme, falling back to defaults for missing keys.
func Load() Colors {
	c := Colors{}
	for k, v := range defaults {
		c[k] = v
	}
	f, err := os.Open(Path())
	if err != nil {
		return c
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		v = strings.Trim(strings.TrimSpace(v), `"`)
		if strings.HasPrefix(v, "#") {
			c[strings.TrimSpace(k)] = v
		}
	}
	return c
}
