package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type settings struct {
	Locked bool    `json:"locked"`
	X      int     `json:"x"`
	Y      int     `json:"y"`
	Zoom   float64 `json:"zoom,omitempty"` // 0 means the default 100%
}

func settingsPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(dir, "TeamoRouterCalendar", "settings.json")
}

func loadSettings() settings {
	// -1 means "no saved position yet"; fall back to the top-right corner.
	s := settings{X: -1, Y: -1}
	path := settingsPath()
	if path == "" {
		return s
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	_ = json.Unmarshal(b, &s)
	return s
}

func saveSettings(s settings) {
	path := settingsPath()
	if path == "" {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0755)
	b, err := json.Marshal(s)
	if err != nil {
		return
	}
	// Replace atomically: a crash mid-write must not lose the lock flag or
	// the saved window position.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0644); err != nil {
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
	}
}
