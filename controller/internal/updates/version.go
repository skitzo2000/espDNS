package updates

import (
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/skitzo2000/espdns/controller/internal/version"
)

// Versions. The plan shows updates as versions ("0.0.1 → 0.0.4"), with the build hash only
// under Details. A firmware's version is what its app descriptor says: the repository's
// VERSION when it was built (internal/version),
// MAJOR.MINOR.PATCH, the same on every chip and board. One whose version isn't one
// (firmware from before versions, whose descriptor says a commit) can't be ordered.

// Scheme is how versions are ordered, for the API: by version alone.
const Scheme = "version"

// Version is one firmware as the pages show it.
type Version struct {
	// Text is what the pages show ("0.0.4").
	Text string `json:"version"`
	// Build is the ELF's SHA-256 (its first 8 bytes, hex, as /status says it). Shown under
	// Details only.
	Build string `json:"build,omitempty"`
	// Built is when it was built, as its descriptor says it ("Oct  7 2026 12:00:00").
	Built string `json:"built,omitempty"`
	sem   version.Semver
	ok    bool // sem was read: Text is a version
}

// maxText is the most of a version shown (the descriptor holds 32 bytes).
const maxText = 32

// Clean is text a node or a build says, as the pages and a change's summary show it: one
// line, no control characters or invalid UTF-8, at most max bytes (cut at a character).
func Clean(s string, max int) string {
	s = strings.TrimSpace(strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == unicode.ReplacementChar {
			return -1
		}
		return r
	}, strings.ToValidUTF8(s, "")))
	for len(s) > max {
		_, n := utf8.DecodeLastRuneInString(s)
		s = s[:len(s)-n]
	}
	return s
}

// Of is the version of a firmware from its app descriptor's version, build time and ELF
// hash (release.AppDesc's, or /status "version", "built", "elf_sha256").
func Of(text, built, elf string) Version {
	text = Clean(text, maxText)
	v := Version{Text: text, Build: strings.ToLower(strings.TrimSpace(elf)), Built: strings.TrimSpace(built)}
	if v.Text == "" {
		v.Text = v.Build
	}
	if sem, err := version.Parse(text); err == nil {
		v.sem, v.ok = sem, true
	}
	return v
}
