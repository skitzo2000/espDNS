// scripts/release/: the release workflow's checks (check-version.sh, changelog.sh), run on a
// copy of the repository's VERSION and CHANGELOG.md. (In pins_test's package: one per directory.)
package pins_test

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"

	"github.com/skitzo2000/espdns/controller/internal/release"
)

func script(t *testing.T, root string, env []string, name string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command("sh", append([]string{"../scripts/release/" + name}, args...)...)
	cmd.Env = append(append(os.Environ(), "ESPDNS_ROOT="+root), env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

const releaseLog = "# Changelog\n\n## [Unreleased]\n\n- Next.\n\n## [0.0.4] - 2026-10-09\n\n" +
	"### Fixed\n\n- A \"quoted\" thing, a back\\slash and a\ttab.\n\n## [0.0.3] - 2026-10-08\n\n- Third.\n\n" +
	"## [0.0.2] - 2026-10-08\n\n## [0.0.1] - 2026-10-07\n\n- First.\n"

func TestChangelogSection(t *testing.T) {
	root := repo(t, "0.0.4\n", releaseLog)
	for v, want := range map[string]string{
		"0.0.4": "### Fixed\n\n- A \"quoted\" thing, a back\\slash and a\ttab.\n",
		"0.0.3": "- Third.\n",
		"0.0.1": "- First.\n",
	} {
		if out, err := script(t, root, nil, "changelog.sh", v); err != nil || out != want {
			t.Errorf("%s: %v %q, want %q", v, err, out, want)
		}
	}
	for v, why := range map[string]string{"0.0.2": "is empty", "0.0.5": "no \"## [0.0.5]", "0.0": "no \"## [0.0]", "Unreleased": "no \"## [Unreleased]"} {
		if out, err := script(t, root, nil, "changelog.sh", v); err == nil || !strings.Contains(out, why) {
			t.Errorf("%s: %v %q, want %q", v, err, out, why)
		}
	}
}

// The tag is v<VERSION>, and the version has its changelog; anything else stops a release.
func TestCheckVersion(t *testing.T) {
	if out, err := script(t, repo(t, "0.0.4\n", releaseLog), nil, "check-version.sh", "v0.0.4"); err != nil || out != "0.0.4\n" {
		t.Fatalf("%v %q", err, out)
	}
	for _, c := range []struct{ version, tag, why string }{
		{"0.0.4\n", "v0.0.3", "VERSION says 0.0.4"},
		{"0.0.4\n", "0.0.4", "VERSION says 0.0.4"},
		{"0.0.4\n", "v0.0.4-rc1", "VERSION says 0.0.4"},
		{"0.0.5\n", "v0.0.5", "no \"## [0.0.5]"},
		{"0.0.2\n", "v0.0.2", "is empty"},
		{"v0.0.4\n", "vv0.0.4", "not MAJOR.MINOR.PATCH"},
	} {
		if out, err := script(t, repo(t, c.version, releaseLog), nil, "check-version.sh", c.tag); err == nil || !strings.Contains(out, c.why) {
			t.Errorf("%q %s: %v %q, want %q", c.version, c.tag, err, out, c.why)
		}
	}
	// The repository's own VERSION has its changelog: tagging it would release
	v, _ := os.ReadFile("../VERSION")
	if out, err := script(t, "..", nil, "check-version.sh", "v"+strings.TrimSpace(string(v))); err != nil {
		t.Errorf("the repository's VERSION: %v %s", err, out)
	}
}

// docs/releasing.md: its commands are docker and git (no make), its ESP-IDF image is the
// pinned one, and the release key's fingerprint it gives is firmware/keys/release.pub's.
func TestReleasingDoc(t *testing.T) {
	doc := read(t, "../docs/releasing.md")
	pub, err := os.ReadFile("../firmware/keys/release.pub")
	if err != nil {
		t.Fatal(err)
	}
	fp := release.Fingerprint(pub)
	if n := strings.Count(doc, "`"+fp+"`"); n < 2 {
		t.Errorf("releasing.md: the release key's fingerprint %s given %d times", fp, n)
	}
	if m := regexp.MustCompile("`[0-9a-f]{16}`").FindAllString(doc, -1); len(m) != strings.Count(doc, "`"+fp+"`") {
		t.Errorf("releasing.md: fingerprints %v, want only %s", m, fp)
	}
	idf := regexp.MustCompile(`(?m)^IDF_IMAGE\s*\?=\s*(\S+)`).FindStringSubmatch(read(t, "../firmware/Makefile"))[1]
	// The commands' ESP-IDF image, here and in the other docs that run it, is the pinned one
	for _, f := range []string{"../docs/releasing.md", "../README.md", "../docs/getting-started.md", "../CONTRIBUTING.md"} {
		for _, img := range regexp.MustCompile(`espressif/idf:\S+@\S+`).FindAllString(read(t, f), -1) {
			if img != idf {
				t.Errorf("%s: %s, not firmware/Makefile's IDF_IMAGE %s", f, img, idf)
			}
		}
	}
	// The firmware build mounts the whole checkout with network: a clean one, never the one
	// with the private keys in it; the version bump needs no network
	if w, b := strings.Index(doc, "git worktree add"), strings.Index(doc, "scripts/release/build-firmware.sh build/release/images"); w < 0 || b < w {
		t.Error("releasing.md: the release built by hand isn't in a new worktree")
	}
	for _, f := range []string{"../docs/releasing.md"} {
		bumps := regexp.MustCompile(`docker run [^\n]*\\\n[^\n]*\\\n[^\n]*bump-version\.sh`).FindAllString(read(t, f), -1)
		if len(bumps) == 0 {
			t.Errorf("%s: no docker command for the version bump", f)
		}
		for _, c := range bumps {
			if !strings.Contains(c, "--network none") {
				t.Errorf("%s: the version bump runs with network: %s", f, c)
			}
		}
	}
	// Each command (continued lines joined, comments dropped, && and | split) is docker or git
	cmds := 0
	for _, block := range regexp.MustCompile("(?s)```sh\n(.*?)```").FindAllStringSubmatch(doc, -1) {
		text := regexp.MustCompile(`\\\n`).ReplaceAllString(block[1], " ")
		for _, l := range strings.Split(text, "\n") {
			l, _, _ = strings.Cut(l, "#")
			for _, c := range regexp.MustCompile(`&&|\|`).Split(l, -1) {
				f := strings.Fields(c)
				if len(f) == 0 {
					continue
				}
				cmds++
				if f[0] != "docker" && f[0] != "git" && !regexp.MustCompile(`^[A-Z]+=\S+$`).MatchString(strings.TrimSpace(c)) {
					t.Errorf("releasing.md: %q: a command other than docker or git", strings.TrimSpace(c))
				}
			}
		}
	}
	if cmds < 10 {
		t.Errorf("releasing.md: %d commands", cmds)
	}
}
