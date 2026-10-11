package keys

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
)

// OldTokenFiles are where older controllers kept a zone primary's API token, in the data
// directory (keys/primary.token, and before it keys/technitium.token). The controller no
// longer talks to a primary's API, so it keeps no token: one left there is removed at
// startup (RemoveOldTokens), never backed up, and skipped by a restore.
var OldTokenFiles = []string{path.Join(Dir, "primary.token"), path.Join(Dir, "technitium.token")}

// IsOldToken says whether rel, a path in the data directory with / separators, is an old
// token file.
func IsOldToken(rel string) bool { return slices.Contains(OldTokenFiles, rel) }

// RemoveOldTokens deletes the old token files in dataDir that are there (a link is removed
// itself, never what it points to), and says which it removed, by their path in the data
// directory.
func RemoveOldTokens(dataDir string) ([]string, error) {
	var removed []string
	var errs []error
	for _, rel := range OldTokenFiles {
		p := filepath.Join(dataDir, filepath.FromSlash(rel))
		if _, err := os.Lstat(p); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err := os.Remove(p); err != nil {
			errs = append(errs, err)
			continue
		}
		removed = append(removed, rel)
	}
	return removed, errors.Join(errs...)
}
