package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/oauth2"
)

type Config struct {
	ClientID     string        `json:"clientId"`
	ClientSecret string        `json:"clientSecret,omitempty"`
	Token        *oauth2.Token `json:"token,omitempty"`
	Email        string        `json:"email,omitempty"`
	OnTop        bool          `json:"onTop"`
	Taskbar      bool          `json:"taskbar"` // show a taskbar button (default: tray only)
	Rect         [4]int32      `json:"rect"`    // x, y, w, h
	SidebarOpen  *bool         `json:"sidebarOpen,omitempty"`
	LastNote     string        `json:"lastNote,omitempty"`

	mu   sync.Mutex
	path string
}

// LoadConfig reads the settings file. It lives in the user's own profile
// (%APPDATA%), which Windows only lets that user's account read.
func LoadConfig(path string) *Config {
	c := &Config{path: path}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, c)
	}
	return c
}

func (c *Config) Update(f func(c *Config)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f(c)
	b, _ := json.MarshalIndent(c, "", " ")
	_ = os.MkdirAll(filepath.Dir(c.path), 0o755)
	tmp := c.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, c.path)
	}
}
