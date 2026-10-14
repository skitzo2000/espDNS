// scripts/release/: the release workflow's checks (check-version.sh, changelog.sh), run on a
// copy of the repository's VERSION files and CHANGELOG.md. (In pins_test's package: one per directory.)
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

const releaseLog = "# Changelog\n\n## [Unreleased]\n\n- Next.\n\n## [firmware 0.0.4] - 2026-10-09\n\n" +
	"### Fixed\n\n- A \"quoted\" thing, a back\\slash and a\ttab.\n\n## [controller 0.0.4] - 2026-10-09\n\n- Controller.\n\n" +
	"## [0.0.3] - 2026-10-08\n\n- Third.\n\n## [0.0.2] - 2026-10-08\n\n## [0.0.1] - 2026-10-07\n\n- First.\n"

func TestChangelogSection(t *testing.T) {
	root := repo(t, "0.0.4\n", releaseLog)
	for v, want := range map[string]string{
		"firmware 0.0.4":   "### Fixed\n\n- A \"quoted\" thing, a back\\slash and a\ttab.\n",
		"controller 0.0.4": "- Controller.\n",
		"0.0.3":            "- Third.\n",
		"0.0.1":            "- First.\n",
	} {
		if out, err := script(t, root, nil, "changelog.sh", v); err != nil || out != want {
			t.Errorf("%s: %v %q, want %q", v, err, out, want)
		}
	}
	for v, why := range map[string]string{"0.0.2": "is empty", "0.0.4": "no \"## [0.0.4]", "firmware 0.0.5": "no \"## [firmware 0.0.5]",
		"controller 0.0.3": "no \"## [controller 0.0.3]", "0.0": "no \"## [0.0]", "Unreleased": "no \"## [Unreleased]"} {
		if out, err := script(t, root, nil, "changelog.sh", v); err == nil || !strings.Contains(out, why) {
			t.Errorf("%s: %v %q, want %q", v, err, out, why)
		}
	}
}

// The tag is firmware-v<firmware/VERSION> or controller-v<controller/VERSION>, and the
// version has its changelog; anything else stops a release.
func TestCheckVersion(t *testing.T) {
	for _, tag := range []string{"firmware-v0.0.4", "controller-v0.0.4"} {
		if out, err := script(t, repo(t, "0.0.4\n", releaseLog), nil, "check-version.sh", tag); err != nil || out != "0.0.4\n" {
			t.Fatalf("%s: %v %q", tag, err, out)
		}
	}
	for _, c := range []struct{ version, tag, why string }{
		{"0.0.4\n", "firmware-v0.0.3", "firmware/VERSION says 0.0.4"},
		{"0.0.4\n", "controller-v0.0.5", "controller/VERSION says 0.0.4"},
		{"0.0.4\n", "v0.0.4", "not firmware-v<version> or controller-v<version>"},
		{"0.0.4\n", "0.0.4", "not firmware-v<version>"},
		{"0.0.4\n", "firmware-v0.0.4-rc1", "VERSION says 0.0.4"},
		{"0.0.5\n", "firmware-v0.0.5", "no \"## [firmware 0.0.5]"},
		{"0.0.3\n", "controller-v0.0.3", "no \"## [controller 0.0.3]"},
		{"v0.0.4\n", "firmware-vv0.0.4", "not MAJOR.MINOR.PATCH"},
	} {
		if out, err := script(t, repo(t, c.version, releaseLog), nil, "check-version.sh", c.tag); err == nil || !strings.Contains(out, c.why) {
			t.Errorf("%q %s: %v %q, want %q", c.version, c.tag, err, out, c.why)
		}
	}
	// Each of the repository's own VERSIONs with a section of its own has its changelog:
	// tagging it would release
	for _, part := range []string{"firmware", "controller"} {
		v, _ := os.ReadFile("../" + part + "/VERSION")
		name := part + " " + strings.TrimSpace(string(v))
		if !strings.Contains(read(t, "../CHANGELOG.md"), "\n## ["+name+"] - ") {
			continue // the version of both, from before each had its own
		}
		if out, err := script(t, "..", nil, "check-version.sh", part+"-v"+strings.TrimSpace(string(v))); err != nil {
			t.Errorf("the repository's %s/VERSION: %v %s", part, err, out)
		}
	}
}

// The release workflows each run on their tag only (firmware-v*, controller-v*) or by hand,
// check the tag first, and never have the signing key: no secret but the forge token, and
// nothing signs.
func TestReleaseWorkflow(t *testing.T) {
	for _, part := range []string{"firmware", "controller"} {
		releaseWorkflow(t, "../.github/workflows/release-"+part+".yml", part, "GITHUB_TOKEN")
	}
	if _, err := os.Stat("../.github/workflows/release.yml"); err == nil {
		t.Error(".github/workflows/release.yml: the release is two workflows, release-firmware.yml and release-controller.yml")
	}
}

func releaseWorkflow(t *testing.T, path, part, secret string) {
	t.Helper()
	wf := read(t, path)
	on := regexp.MustCompile(`(?ms)^on:\n(.*?)\n\S`).FindStringSubmatch(wf)
	if on == nil {
		t.Fatal(path + ": no on:")
	}
	var trig []string
	for _, l := range strings.Split(on[1], "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			trig = append(trig, l)
		}
	}
	if strings.Join(trig, "|") != "push:|tags: ['"+part+"-v*']|workflow_dispatch:" {
		t.Errorf("%s triggers on %q, want a %s-v* tag push and workflow_dispatch only", path, trig, part)
	}
	for _, m := range regexp.MustCompile(`secrets\.([A-Za-z0-9_]+)`).FindAllStringSubmatch(wf, -1) {
		if m[1] != secret {
			t.Errorf("%s: secret %s: the only secret is the forge token %s (the release key never is one)", path, m[1], secret)
		}
	}
	for _, bad := range []string{"release sign", ".pem", "RELEASE_KEY", "keys import", "key import"} {
		if strings.Contains(wf, bad) {
			t.Errorf("%s: %q: CI never signs", path, bad)
		}
	}
	// Every job but the check waits for it
	_, wf, ok := strings.Cut(wf, "\njobs:\n")
	if !ok {
		t.Fatal(path + ": no jobs:")
	}
	jobs := regexp.MustCompile(`(?m)^  ([a-z][a-z0-9-]*):\n`).FindAllStringSubmatchIndex(wf, -1)
	if len(jobs) < 3 {
		t.Fatalf("%s: jobs %v", path, jobs)
	}
	// Each builds its own part only
	if has := strings.Contains(wf, "\n  firmware:\n"); has != (part == "firmware") {
		t.Errorf("%s: a firmware job: %v", path, has)
	}
	if has := strings.Contains(wf, "\n  controller-image:\n"); has != (part == "controller") {
		t.Errorf("%s: a controller-image job: %v", path, has)
	}
	for i, j := range jobs {
		name := wf[j[2]:j[3]]
		end := len(wf)
		if i+1 < len(jobs) {
			end = jobs[i+1][0]
		}
		body := wf[j[1]:end]
		if name == "version" {
			if !strings.Contains(body, "scripts/release/check-version.sh") {
				t.Errorf("%s: the version job doesn't run check-version.sh", path)
			}
			continue
		}
		if !regexp.MustCompile(`(?m)^    needs: (version|\[version[,\]])`).MatchString(body) {
			t.Errorf("%s: job %s doesn't need version", path, name)
		}
	}
}

// GitHub's workflows give the run's token only what each job writes: CI reads, the release
// writes its draft (contents) and the controller's image (packages), each in its own job.
func TestGitHubPermissions(t *testing.T) {
	top := regexp.MustCompile(`(?m)^permissions:(.*)$`)
	write := regexp.MustCompile(`(?m)^\s+([a-z-]+): write$`)
	ci := read(t, "../.github/workflows/ci.yml")
	if m := top.FindStringSubmatch(ci); m == nil || !strings.Contains(ci, "permissions:\n  contents: read\n") {
		t.Error("ci.yml: no top-level permissions: contents: read")
	}
	if w := write.FindAllString(ci, -1); len(w) > 0 || strings.Contains(ci, "write-all") {
		t.Errorf("ci.yml: CI writes nothing, permissions %v", w)
	}
	for _, f := range []string{"release-firmware.yml", "release-controller.yml"} {
		rel := read(t, "../.github/workflows/"+f)
		if m := top.FindStringSubmatch(rel); m == nil || strings.TrimSpace(m[1]) != "{}" {
			t.Error(f + ": the top-level permissions are not {}")
		}
		if strings.Contains(rel, "write-all") {
			t.Error(f + ": write-all")
		}
		_, jobs, _ := strings.Cut(rel, "\njobs:\n")
		idx := regexp.MustCompile(`(?m)^  ([a-z][a-z0-9-]*):\n`).FindAllStringSubmatchIndex(jobs, -1)
		want := map[string]string{"controller-image": "packages", "draft": "contents"}
		for i, j := range idx {
			name := jobs[j[2]:j[3]]
			end := len(jobs)
			if i+1 < len(idx) {
				end = idx[i+1][0]
			}
			var got []string
			for _, m := range write.FindAllStringSubmatch(jobs[j[1]:end], -1) {
				got = append(got, m[1])
			}
			if strings.Join(got, " ") != want[name] {
				t.Errorf("%s: job %s writes %v, want %q", f, name, got, want[name])
			}
		}
	}
}

// docs/releasing.md: its commands are docker and git (no make), its ESP-IDF image is the
// pinned one, and the release key's fingerprint it gives is the project's, keys/project.pub's.
func TestReleasingDoc(t *testing.T) {
	doc := read(t, "../docs/releasing.md")
	pub, err := os.ReadFile("../keys/project.pub")
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

// user-compose.sh: the release's compose.yaml, compose.user.yaml with the controller's image
// by digest, and nothing else changed; anything but a pinned reference is refused.
func TestUserComposeScript(t *testing.T) {
	ref := "ghcr.io/skitzo2000/espdns-controller:0.0.4@sha256:" + strings.Repeat("ab", 32)
	tmpl := read(t, "compose.user.yaml")
	out, err := script(t, t.TempDir(), nil, "user-compose.sh", "compose.user.yaml", ref)
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	if want := strings.Replace(tmpl, `image: "@ESPDNS_IMAGE@"`, "image: "+ref, 1); out != want {
		t.Errorf("rendered:\n%s\nwant:\n%s", out, want)
	}
	for _, bad := range []string{"ghcr.io/skitzo2000/espdns-controller:0.0.4", "espdns-controller@sha256:" + strings.Repeat("ab", 32),
		ref + "\nimage: x", ref + "|x", ""} {
		if out, err := script(t, t.TempDir(), nil, "user-compose.sh", "compose.user.yaml", bad); err == nil {
			t.Errorf("%q: rendered %q", bad, out)
		}
	}
	if out, err := script(t, t.TempDir(), nil, "user-compose.sh", "compose.yaml", ref); err == nil || !strings.Contains(out, "found 0") {
		t.Errorf("compose.yaml, no placeholder: %v %q", err, out)
	}
	wf := "../.github/workflows/release-controller.yml"
	if w := read(t, wf); !strings.Contains(w, "scripts/release/user-compose.sh controller/compose.user.yaml") ||
		!strings.Contains(w, "sha256sum compose.yaml \"$ref\" > SHA256SUMS") {
		t.Errorf("%s: the release doesn't carry compose.yaml", wf)
	}
}
