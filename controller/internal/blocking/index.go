package blocking

// What the last compile of a list read, kept for the Blocking page: each source's counts
// (the domains a blocklist gave) and when, and an index of every entry by its source, so
// "check a site" can name the list that blocks a name.
//
//	blocking/compiled/<list>.json   the report (Report)
//	blocking/compiled/<list>.idx    the index: one line per entry, "name\tflags\tsource",
//	                                sorted, flags "e" (the name only) or "s" (its subdomains
//	                                too) and "a" when it allows; source its index in Specs
//
// Both are written after a compile writes lists/<list>.bin, so they describe the list the
// nodes are sent; a compile refused or failed leaves the last ones. The index is read by a
// binary search on the file, never loaded whole: a big list holds millions of names.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/blocklist"
	"github.com/skitzo2000/espdns/controller/internal/filestore"
	"github.com/skitzo2000/espdns/controller/internal/secfile"
)

// CompiledDir is in Dir: the reports and indexes.
const CompiledDir = "compiled"

func reportPath(dataDir, list string) string {
	return filepath.Join(dataDir, Dir, CompiledDir, list+".json")
}

func indexPath(dataDir, list string) string {
	return filepath.Join(dataDir, Dir, CompiledDir, list+".idx")
}

// Report is what a list's last compile read.
type Report struct {
	List string    `json:"list"`
	Time time.Time `json:"time"`
	// Specs are the definition's sources then its allow sources, in the compile's order,
	// each with a URL's credentials hidden (RedactURL); Sources their counts, in that order.
	Specs   []string                 `json:"specs"`
	Sources []blocklist.SourceResult `json:"sources"`
	// Blocked and Allowed are the names after merging (a name on several lists once).
	Blocked int  `json:"blocked"`
	Allowed int  `json:"allowed"`
	Bytes   int  `json:"bytes"`
	Indexed bool `json:"indexed"` // the index was written with it
}

// Redacted is a spec with a URL's credentials hidden.
func Redacted(spec string) string {
	s, err := blocklist.ParseSource(spec, false)
	if err != nil || !s.IsURL() {
		return spec
	}
	return s.Kind.String() + ":" + RedactURL(s.Path)
}

// Count is the entries a source gave in the report (spec as the definition holds it), and
// whether the report has it.
func (r Report) Count(spec string) (int, bool) {
	red := Redacted(spec)
	for i, s := range r.Specs {
		if s == red && i < len(r.Sources) {
			return r.Sources[i].Entries, true
		}
	}
	return 0, false
}

// IndexEntry is one entry of the index.
type IndexEntry struct {
	Name   string `json:"name"`
	Suffix bool   `json:"suffix"`
	Allow  bool   `json:"allow"`
	Source int    `json:"source"` // in the report's Specs
}

func (e IndexEntry) line() string {
	f := "e"
	if e.Suffix {
		f = "s"
	}
	if e.Allow {
		f += "a"
	}
	return e.Name + "\t" + f + "\t" + strconv.Itoa(e.Source)
}

func parseLine(l string) (IndexEntry, bool) {
	parts := strings.Split(l, "\t")
	if len(parts) != 3 || parts[0] == "" {
		return IndexEntry{}, false
	}
	n, err := strconv.Atoi(parts[2])
	if err != nil || n < 0 {
		return IndexEntry{}, false
	}
	return IndexEntry{Name: parts[0], Suffix: strings.HasPrefix(parts[1], "s"), Allow: strings.HasSuffix(parts[1], "a"), Source: n}, true
}

// WriteCompiled writes a list's report and, with entries (nil: none kept), its index.
func WriteCompiled(dataDir string, l List, res *blocklist.Result, entries []IndexEntry, at time.Time) error {
	if res == nil {
		return errors.New("no result")
	}
	if err := secfile.MkdirAll(filepath.Join(dataDir, Dir, CompiledDir)); err != nil {
		return err
	}
	r := Report{List: l.Name, Time: at.UTC(), Sources: res.Sources, Blocked: res.BlockedExact + res.BlockedSuffix,
		Allowed: res.AllowedExact + res.AllowedSuffix, Bytes: res.File.Size}
	for _, specs := range [][]string{l.Sources, l.Allow} {
		for _, s := range specs {
			r.Specs = append(r.Specs, Redacted(s))
		}
	}
	idx := indexPath(dataDir, l.Name)
	if entries != nil {
		lines := make([]string, 0, len(entries))
		for _, e := range entries {
			lines = append(lines, e.line())
		}
		slices.Sort(lines)
		lines = slices.Compact(lines)
		var b bytes.Buffer
		for _, s := range lines {
			b.WriteString(s)
			b.WriteByte('\n')
		}
		if err := filestore.WriteFile(idx, b.Bytes()); err != nil {
			return err
		}
		r.Indexed = true
	} else {
		os.Remove(idx)
	}
	b, err := json.MarshalIndent(r, "", " ")
	if err != nil {
		return err
	}
	return filestore.WriteFile(reportPath(dataDir, l.Name), append(b, '\n'))
}

// ReadReport is a list's last report (fs.ErrNotExist: none yet).
func ReadReport(dataDir, list string) (Report, error) {
	var r Report
	b, err := os.ReadFile(reportPath(dataDir, list))
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return r, fmt.Errorf("%s: %v", reportPath(dataDir, list), err)
	}
	return r, nil
}

// lookup is every entry of the index for exactly name.
func lookup(f *os.File, size int64, name string) ([]IndexEntry, error) {
	// The first line at or after offset m, its start and its name.
	lineAt := func(m int64) (int64, string, error) {
		start := m
		if m > 0 {
			// Skip to the start of the next line.
			r := bufio.NewReader(io.NewSectionReader(f, m-1, size-m+1))
			skipped, err := r.ReadString('\n')
			if err != nil {
				return size, "", nil
			}
			start = m - 1 + int64(len(skipped))
		}
		if start >= size {
			return size, "", nil
		}
		r := bufio.NewReader(io.NewSectionReader(f, start, size-start))
		l, err := r.ReadString('\n')
		if err != nil && err != io.EOF {
			return 0, "", err
		}
		n, _, _ := strings.Cut(strings.TrimSuffix(l, "\n"), "\t")
		return start, n, nil
	}
	lo, hi := int64(0), size
	for lo < hi {
		mid := lo + (hi-lo)/2
		start, n, err := lineAt(mid)
		if err != nil {
			return nil, err
		}
		if start < size && n < name {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	start, _, err := lineAt(lo)
	if err != nil || start >= size {
		return nil, err
	}
	var out []IndexEntry
	sc := bufio.NewScanner(io.NewSectionReader(f, start, size-start))
	sc.Buffer(make([]byte, 4096), 1<<16)
	for sc.Scan() {
		e, ok := parseLine(sc.Text())
		if !ok || e.Name != name {
			break
		}
		out = append(out, e)
	}
	return out, sc.Err()
}

// Lookup is every entry of the list's index for exactly name; fs.ErrNotExist when the
// list has no index.
func Lookup(dataDir, list, name string) ([]IndexEntry, error) {
	f, err := os.Open(indexPath(dataDir, list))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	return lookup(f, fi.Size(), name)
}

// Verdict is what blocking says about a name.
type Verdict string

const (
	Blocked    Verdict = "blocked"
	Allowed    Verdict = "allowed"
	NotBlocked Verdict = "not blocked"
)

// decide is the node's lookup (firmware block.c, blocklist's decide): the name and each
// parent with two labels or more, most specific first; at the first that matches, an allow
// before a block; an entry for the name only matches the name itself. It returns the
// deciding entries.
func decide(name string, at func(string) ([]IndexEntry, error)) (Verdict, []IndexEntry, error) {
	for i, s := range blocklist.Suffixes(name) {
		es, err := at(s)
		if err != nil {
			return NotBlocked, nil, err
		}
		var allow, block []IndexEntry
		for _, e := range es {
			if i > 0 && !e.Suffix {
				continue
			}
			if e.Allow {
				allow = append(allow, e)
			} else {
				block = append(block, e)
			}
		}
		if len(allow) > 0 {
			return Allowed, allow, nil
		}
		if len(block) > 0 {
			return Blocked, block, nil
		}
	}
	return NotBlocked, nil, nil
}

// Check is a name checked against the blocking the nodes were last sent.
type Check struct {
	Name   string  `json:"name"`
	Result Verdict `json:"result"`
	// By is what decided: "overrides" (the always allowed and blocked sites: they decide
	// first, and still block while blocking is paused), "list", or "" (nothing matched).
	By string `json:"by"`
	// Entries are the deciding entries: a name and whether it covers its subdomains; for
	// the list, the sources that have it (Specs holds them).
	Entries []IndexEntry `json:"entries"`
	Specs   []string     `json:"specs"` // the list's sources, as Report.Specs
	// Indexed says whether the list's sources could be looked at (an index from its last
	// compile); Compiled is when that was.
	List     string    `json:"list,omitempty"`
	Indexed  bool      `json:"indexed"`
	Compiled time.Time `json:"compiled,omitzero"`
}

// overridesAt reads the overrides files as they are (what the nodes were sent): a lookup
// of their wildcard entries.
func overridesAt(dataDir string) (func(string) ([]IndexEntry, error), error) {
	m := map[string][]IndexEntry{}
	for _, f := range []struct {
		name  string
		allow bool
	}{{OverridesBlock, false}, {OverridesAllow, true}} {
		fh, err := os.Open(SourcePath(dataDir, f.name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		_, err = blocklist.Parse(fh, blocklist.Wildcard, func(e blocklist.Entry) {
			m[e.Name] = append(m[e.Name], IndexEntry{Name: e.Name, Suffix: true, Allow: f.allow})
		})
		fh.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %v", f.name, err)
		}
	}
	return func(s string) ([]IndexEntry, error) { return m[s], nil }, nil
}

// CheckName checks a name against the overrides and the list (by its index; "" no list),
// as the nodes decide it.
func CheckName(dataDir, list, raw string) (Check, error) {
	name, ok := blocklist.Canon(strings.TrimSuffix(strings.TrimSpace(raw), "."))
	if !ok {
		return Check{}, fmt.Errorf("%q: not a name a list can hold", raw)
	}
	c := Check{Name: name, Result: NotBlocked, Entries: []IndexEntry{}, Specs: []string{}, List: list}
	ovr, err := overridesAt(dataDir)
	if err != nil {
		return c, err
	}
	v, es, err := decide(name, ovr)
	if err != nil {
		return c, err
	}
	if v != NotBlocked {
		c.Result, c.By, c.Entries = v, OverridesName, es
	}
	if list == "" {
		return c, nil
	}
	rep, rerr := ReadReport(dataDir, list)
	if rerr == nil {
		c.Specs, c.Compiled = rep.Specs, rep.Time
	}
	if c.By != "" {
		return c, nil
	}
	f, err := os.Open(indexPath(dataDir, list))
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return c, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return c, err
	}
	c.Indexed = true
	v, es, err = decide(name, func(s string) ([]IndexEntry, error) { return lookup(f, fi.Size(), s) })
	if err != nil {
		return c, err
	}
	// The list only blocks: an allow entry in it lets the name through, not blocked.
	if v == Blocked {
		c.Result, c.By, c.Entries = Blocked, "list", es
	} else if v == Allowed {
		c.Entries = es
	}
	return c, nil
}
