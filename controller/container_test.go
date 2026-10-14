// The controller's container setup (compose.yaml,
// .dockerignore) for the hardening it must keep: no
// capabilities, no new privileges, a read-only root with a small /tmp, memory and process
// limits, a non-root user; and a build context that is an allowlist, so no data directory,
// whatever it is called, gets into an image. (In pins_test's package: one per directory.)
package pins_test

import (
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

func lines(t *testing.T, path string) []string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, l)
		}
	}
	return out
}

func TestComposeHardening(t *testing.T) {
	compose := strings.Join(lines(t, "compose.yaml"), "\n")
	for what, re := range map[string]string{
		"every capability dropped":                 `(?m)^cap_drop: \[ALL\]$`,
		"no new privileges":                        `(?m)^security_opt: \["no-new-privileges:true"\]$`,
		"a read-only root filesystem":              `(?m)^read_only: true$`,
		"a small /tmp in memory, noexec":           `(?m)^- /tmp:size=\d+m,mode=1777,noexec,nosuid,nodev$`,
		"a memory limit":                           `(?m)^mem_limit: \$\{ESPDNS_MEM_LIMIT:-\d+[mg]\}$`,
		"Go's soft limit under it":                 `(?m)^GOMEMLIMIT: \$\{ESPDNS_GOMEMLIMIT:-\d+MiB\}$`,
		"a process limit":                          `(?m)^pids_limit: \$\{ESPDNS_PIDS_LIMIT:-\d+\}$`,
		"the data directory's owner, required":     `(?m)^user: "\$\{ESPDNS_UID:\?[^}]*\}:\$\{ESPDNS_GID:\?[^}]*\}"$`,
		"the data directory never made by compose": `(?m)^create_host_path: false$`,
	} {
		if !regexp.MustCompile(re).MatchString(compose) {
			t.Errorf("compose.yaml: %s: no line matching %s", what, re)
		}
	}
	for _, bad := range []string{"privileged: true", "cap_add", "docker.sock", "/var/run/docker"} {
		if strings.Contains(compose, bad) {
			t.Errorf("compose.yaml: %s", bad)
		}
	}
	// The memory limit and Go's soft limit, as .env.example documents them, are the defaults
	m := regexp.MustCompile(`mem_limit: \$\{ESPDNS_MEM_LIMIT:-(\d+)g\}`).FindStringSubmatch(compose)
	g := regexp.MustCompile(`GOMEMLIMIT: \$\{ESPDNS_GOMEMLIMIT:-(\d+)MiB\}`).FindStringSubmatch(compose)
	if m == nil || g == nil {
		t.Fatal("compose.yaml: no limits to compare")
	}
	env := read(t, ".env.example") // commented out there, as defaults are
	for _, want := range []string{"#ESPDNS_MEM_LIMIT=" + m[1] + "g", "#ESPDNS_GOMEMLIMIT=" + g[1] + "MiB", "#ESPDNS_PIDS_LIMIT="} {
		if !strings.Contains(env, want) {
			t.Errorf(".env.example: no %s (compose.yaml's default)", want)
		}
	}
	mem, _ := strconv.Atoi(m[1])
	gomem, _ := strconv.Atoi(g[1])
	if mem *= 1024; gomem >= mem || gomem < mem*8/10 {
		t.Errorf("GOMEMLIMIT %d MiB: not about nine tenths of the %d MiB limit", gomem, mem)
	}
}

// The build context is an allowlist: everything left out, then only what the build needs.
func TestDockerignoreAllowlist(t *testing.T) {
	ign := lines(t, ".dockerignore")
	if len(ign) == 0 || ign[0] != "*" {
		t.Fatalf(".dockerignore: first pattern %q, want * (an allowlist)", ign)
	}
	allowed := map[string]bool{}
	for _, l := range ign[1:] {
		if p, ok := strings.CutPrefix(l, "!"); ok {
			allowed[strings.TrimSuffix(p, "/")] = true
		}
	}
	for _, need := range []string{"go.mod", "go.sum", "cmd", "internal", "web"} {
		if !allowed[need] {
			t.Errorf(".dockerignore: %s isn't let in (the Dockerfile's build needs it)", need)
		}
	}
	for p := range allowed {
		if p == "data" || strings.Contains(p, ".env") || strings.Contains(p, "local.mk") || strings.Contains(p, "*") {
			t.Errorf(".dockerignore lets %s in", p)
		}
	}
	for _, never := range []string{"**/*.pem", "**/auth.json"} {
		if !strings.Contains(strings.Join(ign, "\n"), never) {
			t.Errorf(".dockerignore: no %s", never)
		}
	}
}

// The binaries carry controller/VERSION (internal/version): make build and the image both
// link it in, at the package's path; the image's build has it in its own context.
func TestVersionLinked(t *testing.T) {
	const x = "-X github.com/skitzo2000/espdns/controller/internal/version.Version="
	if _, err := os.Stat("internal/version/version.go"); err != nil {
		t.Fatal(err)
	}
	mk := read(t, "Makefile")
	if !strings.Contains(mk, x+"$(shell cat $(CURDIR)/VERSION)") {
		t.Errorf("Makefile: no %s<VERSION>", x)
	}
	for _, bin := range []string{"espdns-controller", "espdns"} {
		if !regexp.MustCompile(`(?m)^\tCGO_ENABLED=0 go build -trimpath -ldflags='\$\(VERSION_LDFLAGS\)' -o ` + bin + ` `).MatchString(mk) {
			t.Errorf("Makefile: %s built without the version", bin)
		}
	}
	df := read(t, "Dockerfile")
	if !strings.Contains(df, "RUN v=$(cat VERSION) && ") {
		t.Error("Dockerfile: VERSION not read")
	}
	if n := strings.Count(df, x+"$v\""); n != 2 {
		t.Errorf("Dockerfile: %d builds link the version, want 2", n)
	}
	if !strings.Contains(strings.Join(lines(t, ".dockerignore"), "\n"), "\n!VERSION\n") {
		t.Error(".dockerignore: VERSION isn't let in")
	}
}

// The image carries the project's release public key at /keys/project.pub (espdns release
// import and fetch check a release with it), from keys/ at the top, a context whose
// .dockerignore lets in that public key only: never a private key, whatever is put there.
// The release workflows build with the same context.
func TestPublicKeysInImage(t *testing.T) {
	if !strings.Contains(read(t, "Dockerfile"), "COPY --from=keys project.pub /keys/") {
		t.Error("Dockerfile: the project's public key not copied from the keys context to /keys")
	}
	if !regexp.MustCompile(`(?m)^ +keys: \.\./keys$`).MatchString(read(t, "compose.yaml")) {
		t.Error("compose.yaml: no keys: ../keys build context")
	}
	if ign := lines(t, "../keys/.dockerignore"); len(ign) != 2 || ign[0] != "*" || ign[1] != "!project.pub" {
		t.Errorf("../keys/.dockerignore: %q, want * and !project.pub only", ign)
	}
	if b, err := os.ReadFile("../keys/project.pub"); err != nil || len(b) != 65 || b[0] != 4 {
		t.Errorf("keys/project.pub: not a raw P-256 public key (%v)", err)
	}
	if w := read(t, "../.github/workflows/release-controller.yml"); !strings.Contains(w, "--build-context keys=keys ") || strings.Contains(w, "firmware/keys") {
		t.Error(".github/workflows/release-controller.yml: the controller image isn't built with --build-context keys=keys")
	}
}

// compose.user.yaml, the release's compose.yaml (scripts/release/user-compose.sh): what a
// user downloads and runs with docker compose up -d, nothing else. compose.yaml's hardening
// and limits, its image only (no build, no .env: nothing to set), its state in a named
// volume, which starts as the image's /data (the Dockerfile: the runtime user's, 0700).
func TestUserCompose(t *testing.T) {
	user := strings.Join(lines(t, "compose.user.yaml"), "\n")
	base := strings.Join(lines(t, "compose.yaml"), "\n")
	def := regexp.MustCompile(`\$\{[A-Z_]+:-([^}]*)\}`) // compose.yaml's defaults, as user's literal values
	for _, key := range []string{"name:", "restart:", "network_mode:", "cap_drop:", "security_opt:", "read_only:",
		"- /tmp:", "mem_limit:", "GOMEMLIMIT:", "pids_limit:", "stop_grace_period:", "interval:", "timeout:", "start_period:", "retries:"} {
		want := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `.*$`).FindString(base)
		want = def.ReplaceAllString(want, "$1")
		if want == "" || !regexp.MustCompile(`(?m)^`+regexp.QuoteMeta(want)+`$`).MatchString(user) {
			t.Errorf("compose.user.yaml: no %q (compose.yaml's)", want)
		}
	}
	for what, re := range map[string]string{
		"the release's image, filled in":     `(?m)^image: "@ESPDNS_IMAGE@"$`,
		"a fixed container name":             `(?m)^container_name: espdns-controller$`,
		"the dashboard on localhost":         `(?m)^command: \["-listen", "127\.0\.0\.1:8480"\]$`,
		"its health checked at that address": `(?m)^test: \["CMD", "/espdns-controller", "-healthcheck", "-listen", "127\.0\.0\.1:8480"\]$`,
		"state in ./data, a bind mount":      `(?m)^source: \./data\ntarget: /data\nbind:\ncreate_host_path: false$`,
		"run as you, from .env":              `(?m)^user: "\$\{ESPDNS_UID:\?[^}]+\}:\$\{ESPDNS_GID:\?[^}]+\}"$`,
	} {
		if !regexp.MustCompile(re).MatchString(user) {
			t.Errorf("compose.user.yaml: %s: no line matching %s", what, re)
		}
	}
	for _, bad := range []string{"build:", "env_file", "privileged", "cap_add", "docker.sock", "/var/run/docker"} {
		if strings.Contains(user, bad) {
			t.Errorf("compose.user.yaml: %s", bad)
		}
	}
	df := read(t, "Dockerfile")
	if !strings.Contains(df, "RUN mkdir -p /rootfs/data && chmod 0700 /rootfs/data") ||
		!strings.Contains(df, "COPY --from=build --chown=65532:65532 /rootfs/ /") {
		t.Error("Dockerfile: /data isn't in the image as the runtime user's (65532), 0700: a Docker volume (the file's other choice) would be root's")
	}
}
