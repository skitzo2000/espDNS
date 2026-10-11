package backup

import (
	"os"
	"path/filepath"
	"strings"
	"time"
)

// LastFile, in the data directory, is when a backup was last written whole, by espdns backup
// or downloaded from the page (RFC 3339): the account panel's "Last backup". Hidden at the
// top, so no backup holds it: a restored controller hasn't been backed up.
const LastFile = ".last-backup"

// Last is when a backup of dataDir was last written whole, or the zero time.
func Last(dataDir string) time.Time {
	b, err := os.ReadFile(filepath.Join(dataDir, LastFile))
	if err != nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(string(b)))
	if err != nil {
		return time.Time{}
	}
	return t
}

// Done records that a backup of dataDir was written whole, now.
func Done(dataDir string) error {
	return os.WriteFile(filepath.Join(dataDir, LastFile), []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600)
}
