package keys

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// The old zone primary token files are removed, a link as itself (what it points to kept);
// nothing else in keys/ is touched; none there: nothing removed, no error.
func TestRemoveOldTokens(t *testing.T) {
	dir := t.TempDir()
	if got, err := RemoveOldTokens(dir); err != nil || len(got) != 0 {
		t.Fatal(got, err)
	}
	os.MkdirAll(filepath.Join(dir, Dir), 0o700)
	other := filepath.Join(dir, "elsewhere")
	os.WriteFile(other, []byte("kept"), 0o600)
	os.WriteFile(filepath.Join(dir, Dir, "primary.token"), []byte("0123456789abcdef"), 0o600)
	os.Symlink(other, filepath.Join(dir, Dir, "technitium.token"))
	os.WriteFile(filepath.Join(dir, Dir, File), []byte("key"), 0o600)
	got, err := RemoveOldTokens(dir)
	if err != nil || !slices.Equal(got, []string{"keys/primary.token", "keys/technitium.token"}) {
		t.Fatal(got, err)
	}
	for _, rel := range OldTokenFiles {
		if _, err := os.Lstat(filepath.Join(dir, rel)); !os.IsNotExist(err) {
			t.Fatal(rel, "left:", err)
		}
	}
	for _, p := range []string{other, filepath.Join(dir, Dir, File)} {
		if _, err := os.Stat(p); err != nil {
			t.Fatal(p, err)
		}
	}
	if !IsOldToken("keys/primary.token") || IsOldToken("keys/release.pem") {
		t.Fatal("IsOldToken")
	}
}
