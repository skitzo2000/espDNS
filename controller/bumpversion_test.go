// scripts/bump-version.sh, the one way a version moves (docs/releasing.md): run on a copy
// of the repository's VERSION files and CHANGELOG.md. (In pins_test's package: one per directory.)
package pins_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func bump(t *testing.T, root string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("sh", append([]string{"../scripts/bump-version.sh"}, args...)...)
	cmd.Env = append(os.Environ(), "ESPDNS_ROOT="+root)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// repo is a repository with firmware/VERSION and controller/VERSION both version.
func repo(t *testing.T, version, changelog string) string {
	t.Helper()
	root := t.TempDir()
	for name, s := range map[string]string{"firmware/VERSION": version, "controller/VERSION": version, "CHANGELOG.md": changelog} {
		os.MkdirAll(filepath.Join(root, filepath.Dir(name)), 0o755)
		if err := os.WriteFile(filepath.Join(root, name), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

const changelog = "# Changelog\n\nIntro.\n\n## [Unreleased]\n\n### Fixed\n\n- A thing.\n\n## [0.0.1] - 2026-10-07\n\n- First.\n"

func TestBumpVersion(t *testing.T) {
	for _, c := range []struct{ part, from, arg, want string }{
		{"firmware", "0.0.1", "patch", "0.0.2"}, {"firmware", "0.0.9", "patch", "0.0.10"}, {"controller", "0.0.4", "minor", "0.1.0"},
		{"controller", "0.3.4", "major", "1.0.0"}, {"firmware", "0.0.1", "0.0.4", "0.0.4"}, {"controller", "0.0.9", "0.1.0", "0.1.0"},
	} {
		root := repo(t, c.from+"\n", changelog)
		if out, err := bump(t, root, c.part, c.arg); err != nil {
			t.Errorf("%s %s %s: %v %s", c.part, c.from, c.arg, err, out)
			continue
		}
		if b, _ := os.ReadFile(filepath.Join(root, c.part, "VERSION")); string(b) != c.want+"\n" {
			t.Errorf("%s %s %s: VERSION %q, want %s", c.part, c.from, c.arg, b, c.want)
		}
		other := map[string]string{"firmware": "controller", "controller": "firmware"}[c.part]
		if b, _ := os.ReadFile(filepath.Join(root, other, "VERSION")); string(b) != c.from+"\n" {
			t.Errorf("%s %s %s: %s/VERSION now %q", c.part, c.from, c.arg, other, b)
		}
		b, _ := os.ReadFile(filepath.Join(root, "CHANGELOG.md"))
		want := regexp.MustCompile(`(?s)^# Changelog\n\nIntro\.\n\n## \[Unreleased\]\n\n## \[` + c.part + " " + regexp.QuoteMeta(c.want) +
			`\] - \d{4}-\d{2}-\d{2}\n\n### Fixed\n\n- A thing\.\n\n## \[0\.0\.1\] - 2026-10-07\n\n- First\.\n$`)
		if !want.Match(b) {
			t.Errorf("%s %s %s: CHANGELOG.md\n%s", c.part, c.from, c.arg, b)
		}
	}
}

// Refused, and nothing written: not a newer version, a VERSION that isn't one, no changelog
// for the version, no part named.
func TestBumpVersionRefused(t *testing.T) {
	for _, c := range []struct {
		version, changelog string
		args               []string
		why                string
	}{
		{"0.0.4\n", changelog, []string{"firmware", "0.0.4"}, "not newer"},
		{"0.0.4\n", changelog, []string{"controller", "0.0.3"}, "not newer"},
		{"0.1.0\n", changelog, []string{"firmware", "0.0.9"}, "not newer"},
		{"0.0.4\n", changelog, []string{"firmware", "v0.0.5"}, "usage"},
		{"0.0.4\n", changelog, []string{"firmware", "0.0.05"}, "usage"},
		{"0.0.4\n", changelog, []string{"firmware", ""}, "usage"},
		{"0.0.4\n", changelog, []string{"patch"}, "usage"},
		{"0.0.4\n", changelog, []string{"both", "patch"}, "usage"},
		{"0.0.4\n", changelog, []string{"firmware", "0.0.99999999999999999999"}, "usage"},
		{"0.0.4\n", changelog, []string{"firmware", "0.0.1000000000"}, "usage"},
		{"0.0.999999999\n", changelog, []string{"firmware", "patch"}, "more than 9 digits"},
		{"0.0.1000000000\n", changelog, []string{"controller", "patch"}, "not MAJOR.MINOR.PATCH"},
		{"3fbae0a\n", changelog, []string{"firmware", "patch"}, "not MAJOR.MINOR.PATCH"},
		{"0.0.4\n", "# Changelog\n\n## [0.0.4] - 2026-10-07\n", []string{"firmware", "patch"}, "no \"## [Unreleased]\""},
		{"0.0.4\n", "# Changelog\n\n## [Unreleased]\n\n## [0.0.4] - 2026-10-07\n\n- x\n", []string{"firmware", "patch"}, "is empty"},
	} {
		root := repo(t, c.version, c.changelog)
		out, err := bump(t, root, c.args...)
		if err == nil || !strings.Contains(out, c.why) {
			t.Errorf("%q %q %s: %v %q, want %q", c.version, c.args, c.changelog, err, out, c.why)
		}
		for _, part := range []string{"firmware", "controller"} {
			if b, _ := os.ReadFile(filepath.Join(root, part, "VERSION")); string(b) != c.version {
				t.Errorf("%q %q: %s/VERSION now %q", c.version, c.args, part, b)
			}
		}
		if b, _ := os.ReadFile(filepath.Join(root, "CHANGELOG.md")); string(b) != c.changelog {
			t.Errorf("%q %q: CHANGELOG.md changed", c.version, c.args)
		}
		if m, _ := filepath.Glob(filepath.Join(root, ".bump.*")); len(m) > 0 {
			t.Errorf("left %v", m)
		}
	}
}

// The repository's own CHANGELOG.md has the section the script needs, and an entry for each
// VERSION: "## [firmware 0.0.3]", or the version alone from when the two shared one.
func TestChangelogHasVersion(t *testing.T) {
	log := read(t, "../CHANGELOG.md")
	if !strings.Contains(log, "\n## [Unreleased]\n") {
		t.Error("CHANGELOG.md: no ## [Unreleased]")
	}
	for _, part := range []string{"firmware", "controller"} {
		b, err := os.ReadFile("../" + part + "/VERSION")
		if err != nil {
			t.Fatal(err)
		}
		v := strings.TrimSpace(string(b))
		if !strings.Contains(log, "\n## ["+part+" "+v+"] - ") && !strings.Contains(log, "\n## ["+v+"] - ") {
			t.Errorf("CHANGELOG.md: no ## [%s %s]", part, v)
		}
	}
}
