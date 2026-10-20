package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/skitzo2000/espdns/controller/internal/keys"
	"github.com/skitzo2000/espdns/controller/internal/settings"
)

// The zone primary in the settings: its address. An older "primary" (an API's kind, address
// and pin, the networks it allowed) is taken and those fields dropped.
func TestSettingsPrimary(t *testing.T) {
	e := newSetupEnv(t)
	e.firstPassword()
	code, out := e.do("POST /api/settings", `{"settings": {"nodes": ["192.0.2.53", "198.51.100.9", "dns3.local"],
		"primary": {"address": "192.0.2.254"}}, "version": ""}`)
	if code != http.StatusOK {
		t.Fatal(code, out)
	}
	p := out["primary"].(map[string]any)
	if p["address"] != "192.0.2.254" || p["allows"] != nil || p["uncovered"] != nil {
		t.Fatalf("primary: %v", p)
	}
	old := `{"settings": {"primary": {"kind": "technitium", "url": "https://192.0.2.254:53443", "cert_sha256": "` +
		strings.Repeat("ab", 32) + `", "allows": ["192.0.2.0/24"]}}, "version": "` + out["version"].(string) + `"}`
	code, out = e.do("POST /api/settings", old)
	if code != http.StatusOK {
		t.Fatal(code, out)
	}
	b, _ := os.ReadFile(settings.Path(e.dir))
	if !strings.Contains(string(b), `"address": "192.0.2.254"`) || strings.Contains(string(b), "kind") || strings.Contains(string(b), "cert_sha256") ||
		strings.Contains(string(b), "allows") {
		t.Fatalf("written: %s", b)
	}
}

// At startup: the token file an older controller kept for a zone primary's API is deleted,
// saying to revoke it; an older "primary" loads, its API fields said once to be ignored.
func TestStartupRemovesOldToken(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "keys", "primary.token"), []byte("0123456789abcdef"))
	writeFile(t, filepath.Join(dir, "keys", "release.pem"), []byte("kept"))
	writeFile(t, settings.Path(dir), []byte(`{"nodes": ["192.0.2.53"], "primary": {"kind": "technitium", "url": "https://192.0.2.254:53443", "allows": ["192.0.2.0/24"]}}`))
	var logs []string
	st, err := loadSettings(dir, func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) })
	if err != nil || st.ZonePrimary().Address != "192.0.2.254" {
		t.Fatal(st, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "keys", "primary.token")); !os.IsNotExist(err) {
		t.Fatal("the token is still there:", err)
	}
	if _, err := os.Stat(keys.Path(dir)); err != nil {
		t.Fatal("the release key went too:", err)
	}
	if len(logs) != 2 || !strings.Contains(logs[0], "removed keys/primary.token, the unused zone primary API token: revoke that token") ||
		!strings.Contains(logs[1], `"primary" fields kind, url, allows are no longer used`) {
		t.Fatalf("%q", logs)
	}
	// Once gone, nothing more is said about it.
	logs = nil
	writeFile(t, settings.Path(dir), []byte(`{"nodes": ["192.0.2.53"]}`))
	if _, err := loadSettings(dir, func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }); err != nil ||
		slices.ContainsFunc(logs, func(l string) bool { return strings.Contains(l, "token") || strings.Contains(l, "primary") }) {
		t.Fatal(err, logs)
	}
}
