package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"golang.org/x/oauth2"
)

type Config struct {
	ClientID     string        `json:"clientId"`
	ClientSecret string        `json:"-"`
	Token        *oauth2.Token `json:"-"`
	// Sealed holds ClientSecret+Token, encrypted for the current Windows user (DPAPI).
	Sealed       string        `json:"sealed,omitempty"`
	LegacySecret string        `json:"clientSecret,omitempty"` // read once, then sealed
	LegacyToken  *oauth2.Token `json:"token,omitempty"`        // read once, then sealed
	Email        string        `json:"email,omitempty"`
	OnTop        bool          `json:"onTop"`
	Rect         [4]int32      `json:"rect"` // x, y, w, h
	SidebarOpen  *bool         `json:"sidebarOpen,omitempty"`
	LastNote     string        `json:"lastNote,omitempty"`

	mu   sync.Mutex
	path string
}

func LoadConfig(path string) *Config {
	c := &Config{path: path}
	if b, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(b, c)
	}
	c.ClientSecret, c.Token = c.LegacySecret, c.LegacyToken
	if c.Sealed != "" {
		if raw, err := base64.StdEncoding.DecodeString(c.Sealed); err == nil {
			if plain, err := unprotect(raw); err == nil {
				var sec secrets
				if json.Unmarshal(plain, &sec) == nil {
					c.ClientSecret, c.Token = sec.ClientSecret, sec.Token
				}
			}
		}
	}
	if c.LegacySecret != "" || c.LegacyToken != nil {
		c.Update(func(*Config) {}) // re-save sealed, drop plain-text copies
	}
	return c
}

type secrets struct {
	ClientSecret string        `json:"s"`
	Token        *oauth2.Token `json:"t"`
}

// protect/unprotect encrypt secrets at rest; Windows sets them to DPAPI.
var (
	protect   = func(b []byte) ([]byte, error) { return b, nil }
	unprotect = func(b []byte) ([]byte, error) { return b, nil }
)

func (c *Config) Update(f func(c *Config)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f(c)
	c.LegacySecret, c.LegacyToken, c.Sealed = "", nil, ""
	if c.ClientSecret != "" || c.Token != nil {
		plain, _ := json.Marshal(secrets{c.ClientSecret, c.Token})
		if enc, err := protect(plain); err == nil {
			c.Sealed = base64.StdEncoding.EncodeToString(enc)
		}
	}
	b, _ := json.MarshalIndent(c, "", " ")
	_ = os.MkdirAll(filepath.Dir(c.path), 0o755)
	tmp := c.path + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, c.path)
	}
}
