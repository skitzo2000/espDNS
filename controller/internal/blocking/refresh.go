package blocking

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/blocklist"
	"github.com/skitzo2000/espdns/controller/internal/filestore"
	"github.com/skitzo2000/espdns/controller/internal/secfile"
)

// The scheduled refresh's state (blocking/refresh.json): for each list with a URL source,
// when it was last checked and last sent to the nodes changed, and each URL source's
// version the nodes were last sent (its content's hash, and the server's validators for
// the next conditional GET). The controller writes it; nothing else reads it. A file that
// can't be read is started again (each list checked once more), never an error that stops
// the refresh.
const RefreshFile = "refresh.json"

// RetryAfter is how soon a check that failed (a server down) is tried again, when the
// schedule is longer.
const RetryAfter = time.Hour

// MaxJitter is the most a list's check is put off past its time, so the lists (and the
// controllers on one schedule) don't all fetch at once: a twentieth of the schedule, at
// most this.
const MaxJitter = 30 * time.Minute

// The outcomes of a list's last check.
const (
	Unchanged = "unchanged" // its servers have the version the nodes have
	Updating  = "updating"  // changed: a refresh job is sending it
	Updated   = "updated"   // changed, and sent to the nodes
	Refused   = "refused"   // changed by more than its size-change limit: not sent
	Failed    = "failed"    // a fetch, or sending it, failed
	Waiting   = "waiting"   // a pending change edits the list: the next apply fetches it
)

// SourceRefresh is a URL source's version the nodes were last sent.
type SourceRefresh struct {
	blocklist.Fetched
	Updated time.Time `json:"updated,omitzero"` // when that version was first sent
	Checked time.Time `json:"checked,omitzero"` // when it was last fetched or asked about
}

// ListRefresh is one list's refresh.
type ListRefresh struct {
	Checked time.Time `json:"checked,omitzero"` // the last check of its URL sources
	Updated time.Time `json:"updated,omitzero"` // the last time a changed version went out
	JitterS int       `json:"jitter_s,omitempty"`
	Outcome string    `json:"outcome,omitempty"`
	Message string    `json:"message,omitempty"` // why, for refused, failed, waiting (credentials hidden)
	// Refused is the version (SourcesSum) the size-change check refused, Over its words:
	// that same version isn't tried again by the schedule, only a newer one.
	Refused string   `json:"refused,omitempty"`
	Over    []string `json:"over,omitempty"`
	// Sources are the URL sources' versions the nodes were last sent, by the source's spec.
	Sources map[string]*SourceRefresh `json:"sources,omitempty"`
}

// Refresh is refresh.json.
type Refresh struct {
	Lists map[string]*ListRefresh `json:"lists"`
}

// List is the list's state (an empty one, added, when there is none).
func (r *Refresh) List(name string) *ListRefresh {
	if r.Lists == nil {
		r.Lists = map[string]*ListRefresh{}
	}
	l := r.Lists[name]
	if l == nil {
		l = &ListRefresh{}
		r.Lists[name] = l
	}
	if l.Sources == nil {
		l.Sources = map[string]*SourceRefresh{}
	}
	return l
}

func refreshPath(dataDir string) string { return filepath.Join(dataDir, Dir, RefreshFile) }

// refreshMu orders the read-modify-writes of refresh.json in this process.
var refreshMu sync.Mutex

// LoadRefresh reads refresh.json; none, or one that can't be read, is empty.
func LoadRefresh(dataDir string) Refresh {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	return loadRefresh(dataDir)
}

func loadRefresh(dataDir string) Refresh {
	r := Refresh{Lists: map[string]*ListRefresh{}}
	b, err := os.ReadFile(refreshPath(dataDir))
	if err != nil {
		return r
	}
	if json.Unmarshal(b, &r) != nil || r.Lists == nil {
		return Refresh{Lists: map[string]*ListRefresh{}}
	}
	return r
}

// UpdateRefresh changes refresh.json through f, written whole.
func UpdateRefresh(dataDir string, f func(*Refresh)) error {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	r := loadRefresh(dataDir)
	f(&r)
	b, err := json.MarshalIndent(r, "", " ")
	if err != nil {
		return err
	}
	if err := secfile.MkdirAll(Path(dataDir)); err != nil {
		return err
	}
	return filestore.WriteFile(refreshPath(dataDir), append(b, '\n'))
}

// URLSources are the list's sources fetched from a URL (blocked and allowed), as specs.
func (l List) URLSources() []string {
	var out []string
	for _, spec := range append(slices.Clone(l.Sources), l.Allow...) {
		if s, err := blocklist.ParseSource(spec, false); err == nil && s.IsURL() {
			out = append(out, spec)
		}
	}
	return out
}

// Compiled is when the list was last compiled (its report's time; zero: never).
func Compiled(dataDir, list string) time.Time {
	if rep, err := ReadReport(dataDir, list); err == nil {
		return rep.Time
	}
	if fi, err := os.Stat(Out(dataDir, list)); err == nil {
		return fi.ModTime()
	}
	return time.Time{}
}

// Jitter is a list's delay past its time: r (0 to 1) of a twentieth of every, at most
// MaxJitter.
func Jitter(every time.Duration, r float64) int {
	return int(r * float64(min(every/20, MaxJitter)) / float64(time.Second))
}

// Next is when the list is checked next, on a schedule of every (0: off, never):
// every after its last check (its last compile, before any check; neither: now), or
// RetryAfter after a failed one, plus its jitter.
func (l *ListRefresh) Next(every time.Duration, compiled time.Time) time.Time {
	if every <= 0 {
		return time.Time{}
	}
	last := l.Checked
	if last.IsZero() {
		last = compiled
	}
	if last.IsZero() {
		return time.Time{} // never fetched: due now
	}
	wait := every
	if l.Outcome == Failed && RetryAfter < wait {
		wait = RetryAfter
	}
	return last.Add(wait + time.Duration(l.JitterS)*time.Second)
}

// Due says whether the list's check is due at now.
func (l *ListRefresh) Due(every time.Duration, compiled, now time.Time) bool {
	return every > 0 && !l.Next(every, compiled).After(now)
}

// SourcesSum is one hash for a list's URL sources' versions (by spec), to compare a
// version refused with the next one.
func SourcesSum(v map[string]string) string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	h := sha256.New()
	for _, k := range keys {
		fmt.Fprintf(h, "%s\x00%s\n", k, v[k])
	}
	return hex.EncodeToString(h.Sum(nil))
}

// FetchedOf are the URL sources' versions a compile fetched, by spec (the list's sources,
// then its allow sources, as Request orders them).
func (l List) FetchedOf(res *blocklist.Result) map[string]blocklist.Fetched {
	out := map[string]blocklist.Fetched{}
	if res == nil {
		return out
	}
	specs := append(slices.Clone(l.Sources), l.Allow...)
	for i, sr := range res.Sources {
		if i < len(specs) && sr.Fetched != nil {
			out[specs[i]] = *sr.Fetched
		}
	}
	return out
}

// Sent records that the nodes were sent the list compiled from these versions at now
// (a refresh's, or an apply's): the sources whose version changed are updated now, the
// refusal is cleared, the next check is every (jitter r) from now. Sources no longer in
// the list are forgotten.
func (r *Refresh) Sent(l List, fetched map[string]blocklist.Fetched, now time.Time, every time.Duration, jr float64) {
	if len(l.URLSources()) == 0 {
		delete(r.Lists, l.Name)
		return
	}
	ls := r.List(l.Name)
	changed := false
	for spec, f := range fetched {
		s := ls.Sources[spec]
		if s == nil {
			s = &SourceRefresh{}
			ls.Sources[spec] = s
		}
		if s.SHA256 != f.SHA256 {
			s.Updated, changed = now, true
		}
		s.Fetched, s.Checked = f, now
	}
	ls.prune(l)
	ls.Checked, ls.JitterS, ls.Message, ls.Refused, ls.Over = now, Jitter(every, jr), "", "", nil
	ls.Outcome = Unchanged
	if changed {
		ls.Outcome, ls.Updated = Updated, now
	}
}

// prune forgets the sources no longer in the list.
func (ls *ListRefresh) prune(l List) {
	urls := l.URLSources()
	for spec := range ls.Sources {
		if !slices.Contains(urls, spec) {
			delete(ls.Sources, spec)
		}
	}
}

// Checked records a check that found the list as the nodes have it (same: the servers' new
// validators kept, a refusal forgotten), or as the version refused before (it stays
// refused).
func (r *Refresh) Checked(l List, fetched map[string]blocklist.Fetched, same bool, now time.Time, every time.Duration, jr float64) {
	ls := r.List(l.Name)
	for spec, f := range fetched {
		if s := ls.Sources[spec]; s != nil && s.SHA256 == f.SHA256 {
			s.Fetched, s.Checked = f, now
		}
	}
	ls.prune(l)
	ls.Checked, ls.JitterS = now, Jitter(every, jr)
	if same {
		ls.Outcome, ls.Message, ls.Refused, ls.Over = Unchanged, "", "", nil
	}
}

// Outcome records a check's or a refresh's outcome but its versions: updating, refused
// (with the version refused and its words), failed or waiting, and why.
func (r *Refresh) Outcome(list, outcome, message string, now time.Time, every time.Duration, jr float64) {
	ls := r.List(list)
	ls.Outcome, ls.Message = outcome, message
	if outcome != Waiting {
		ls.Checked, ls.JitterS = now, Jitter(every, jr)
	}
	if outcome != Refused {
		ls.Refused, ls.Over = "", nil
	}
}

// Refuse records the version refused by the size-change check, and its words.
func (r *Refresh) Refuse(l List, fetched map[string]blocklist.Fetched, over []string, now time.Time, every time.Duration, jr float64) {
	v := map[string]string{}
	for spec, f := range fetched {
		v[spec] = f.SHA256
	}
	r.Outcome(l.Name, Refused, "changed by more than its size-change limit: "+strings.Join(over, ", "), now, every, jr)
	ls := r.List(l.Name)
	ls.Refused, ls.Over = SourcesSum(v), over
}
