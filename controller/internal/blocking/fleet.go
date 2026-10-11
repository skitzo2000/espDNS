package blocking

// Blocking as one setup for the whole fleet: the
// same blocklists, allowed and blocked sites on every node; per node it is only on, off or
// paused. The blocklists are the sources of one list definition, the fleet's (Fleet), which
// the Blocking page edits: a list added, removed, or turned off and on again (kept in its
// Unused sources). The allowed and blocked sites are the overrides' two files. Each edit is
// a new text for one file, which the page adds to the pending changes ("collect, then
// Apply"); the apply compiles and sends it.

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"

	"github.com/skitzo2000/espdns/controller/internal/blocklist"
)

// FleetName is the fleet's list when the definitions have none yet: lists/main.bin.
const FleetName = "main"

// Fleet is the fleet's list: the first definition (any others are left as they are), and
// its index (-1: none yet).
func (d Defs) Fleet() (List, int) {
	if len(d.Lists) == 0 {
		return List{Name: FleetName}, -1
	}
	return d.Lists[0], 0
}

// ErrNoChange: the edit changes nothing.
var ErrNoChange = errors.New("that is how it is already: nothing to change")

// SourceTitle is a source in words: the catalogue's name for it, else the host of its URL,
// else its file.
func SourceTitle(spec string) string {
	if c, ok := CatalogFor(spec); ok {
		return c.Name
	}
	s, err := blocklist.ParseSource(spec, false)
	if err != nil {
		return spec
	}
	if s.IsURL() {
		if u, err := url.Parse(s.Path); err == nil && u.Host != "" {
			return u.Hostname()
		}
	}
	return s.Path
}

func titles(specs []string) string {
	var out []string
	for _, s := range specs {
		out = append(out, SourceTitle(s))
	}
	return strings.Join(out, ", ")
}

// EditFleet edits the fleet's list: op "add" (specs to block, each new), "remove", "use"
// or "unuse" (one spec). It returns the definitions after, and the edit in words. The last
// list in use isn't removed or turned off: blocking is turned off in a node's settings, or
// paused.
func EditFleet(d Defs, op string, specs []string) (Defs, string, error) {
	l, i := d.Fleet()
	l.Sources, l.Allow, l.Unused = slices.Clone(l.Sources), slices.Clone(l.Allow), slices.Clone(l.Unused)
	has := func(s string) bool {
		return slices.Contains(l.Sources, s) || slices.Contains(l.Allow, s) || slices.Contains(l.Unused, s)
	}
	var summary string
	switch op {
	case "add":
		if len(specs) == 0 {
			return d, "", errors.New("add: no list given")
		}
		for _, s := range specs {
			if err := CheckSource(s); err != nil {
				return d, "", err
			}
			if slices.Contains(l.Unused, s) {
				return d, "", fmt.Errorf("%s is in your blocklists, not in use: turn it on there", SourceTitle(s))
			}
			if has(s) {
				return d, "", fmt.Errorf("%s is in your blocklists already", SourceTitle(s))
			}
			l.Sources = append(l.Sources, s)
		}
		if len(specs) == 1 {
			summary = "Add blocklist " + SourceTitle(specs[0])
		} else {
			summary = fmt.Sprintf("Add %d blocklists: %s", len(specs), titles(specs))
		}
	case "remove", "use", "unuse":
		if len(specs) != 1 {
			return d, "", fmt.Errorf("%s: one list", op)
		}
		s := specs[0]
		if !has(s) {
			return d, "", fmt.Errorf("%s is not in your blocklists", SourceTitle(s))
		}
		inUse := slices.Contains(l.Sources, s)
		switch op {
		case "remove":
			l.Sources = slices.DeleteFunc(l.Sources, func(x string) bool { return x == s })
			l.Allow = slices.DeleteFunc(l.Allow, func(x string) bool { return x == s })
			l.Unused = slices.DeleteFunc(l.Unused, func(x string) bool { return x == s })
			summary = "Remove blocklist " + SourceTitle(s)
		case "use":
			if inUse {
				return d, "", ErrNoChange
			}
			l.Unused = slices.DeleteFunc(l.Unused, func(x string) bool { return x == s })
			l.Sources = append(l.Sources, s)
			summary = "Use blocklist " + SourceTitle(s)
		case "unuse":
			if !inUse {
				return d, "", ErrNoChange
			}
			l.Sources = slices.DeleteFunc(l.Sources, func(x string) bool { return x == s })
			l.Unused = append(l.Unused, s)
			summary = "Stop using blocklist " + SourceTitle(s)
		}
		if inUse && len(l.Sources) == 0 {
			return d, "", errors.New("it is the last list in use: to stop blocking, pause it, or turn blocking off in each node's settings")
		}
	default:
		return d, "", fmt.Errorf("op %q: add, remove, use or unuse", op)
	}
	out := Defs{Lists: slices.Clone(d.Lists)}
	if i < 0 {
		out.Lists = append([]List{l}, out.Lists...)
	} else {
		out.Lists[i] = l
	}
	if out.Lists == nil {
		out.Lists = []List{}
	}
	return out, summary, out.Check()
}

// Site is a site as the always allowed or blocked sites take it: a name a list can hold.
func Site(raw string) (string, error) {
	s := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	s = strings.TrimPrefix(s, "*.")
	if u, err := url.Parse(s); err == nil && u.Host != "" && strings.Contains(s, "://") {
		s = u.Hostname() // a site's address pasted
	}
	n, ok := blocklist.Canon(s)
	if !ok || !strings.Contains(n, ".") {
		return "", fmt.Errorf("%q: a site's name, such as ads.example.com", strings.TrimSpace(raw))
	}
	return n, nil
}

// Sites are the names in an overrides file, in its order, each once.
func Sites(text []byte) []string {
	var out []string
	blocklist.Parse(strings.NewReader(string(text)), blocklist.Wildcard, func(e blocklist.Entry) {
		if !slices.Contains(out, e.Name) {
			out = append(out, e.Name)
		}
	})
	return out
}

// EditSites adds a site to an overrides file's text, or takes it out (every line naming
// it, its comments kept). ErrNoChange when it is already so.
func EditSites(text []byte, site string, add bool) ([]byte, error) {
	if slices.Contains(Sites(text), site) == add {
		return nil, ErrNoChange
	}
	if add {
		t := string(text)
		if t != "" && !strings.HasSuffix(t, "\n") {
			t += "\n"
		}
		return []byte(t + site + "\n"), nil
	}
	var keep []string
	for _, line := range strings.SplitAfter(string(text), "\n") {
		if line == "" {
			continue
		}
		body := strings.TrimSpace(line)
		if i := strings.IndexByte(body, '#'); i >= 0 {
			body = strings.TrimSpace(body[:i])
		}
		if n, ok := blocklist.Canon(strings.TrimPrefix(body, "*.")); ok && n == site {
			continue
		}
		keep = append(keep, line)
	}
	return []byte(strings.Join(keep, "")), nil
}
