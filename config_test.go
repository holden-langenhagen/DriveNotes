package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/oauth2"
)

func TestConfigSealsSecrets(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.json")
	// an old config with plain-text secrets is migrated to the sealed form
	os.WriteFile(p, []byte(`{"clientId":"id","clientSecret":"SECRET-X","token":{"access_token":"TOKEN-Y","refresh_token":"REFRESH-Z"}}`), 0o600)
	xor := func(b []byte) ([]byte, error) { // reversible stand-in for DPAPI
		o := make([]byte, len(b))
		for i := range b {
			o[i] = b[i] ^ 0x5a
		}
		return o, nil
	}
	protect, unprotect = xor, xor
	defer func() {
		protect = func(b []byte) ([]byte, error) { return b, nil }
		unprotect = protect
	}()
	c := LoadConfig(p)
	if c.ClientSecret != "SECRET-X" || c.Token.AccessToken != "TOKEN-Y" {
		t.Fatalf("legacy secrets not loaded: %+v", c)
	}
	raw, _ := os.ReadFile(p)
	for _, leak := range []string{"SECRET-X", "TOKEN-Y", "REFRESH-Z"} {
		if strings.Contains(string(raw), leak) {
			t.Fatalf("plain-text secret %q left in config: %s", leak, raw)
		}
	}
	c.Update(func(c *Config) { c.Token = &oauth2.Token{AccessToken: "a2"} })
	if c2 := LoadConfig(p); c2.Token == nil || c2.Token.AccessToken != "a2" || c2.ClientSecret != "SECRET-X" {
		t.Fatalf("sealed round trip failed: %+v", c2)
	}
}
