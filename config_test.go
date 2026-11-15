package main

import (
	"path/filepath"
	"testing"

	"golang.org/x/oauth2"
)

func TestConfigRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	c := LoadConfig(p)
	c.Update(func(c *Config) {
		c.ClientID, c.ClientSecret, c.Taskbar = "id", "sec", true
		c.Token = &oauth2.Token{AccessToken: "a", RefreshToken: "r"}
	})
	c2 := LoadConfig(p)
	if c2.ClientSecret != "sec" || c2.Token == nil || c2.Token.RefreshToken != "r" || !c2.Taskbar {
		t.Fatalf("round trip failed: %+v", c2)
	}
}
