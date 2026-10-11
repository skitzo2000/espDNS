package blocklist

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

// Check sends the validators it was given: a 304 is unchanged with nothing fetched; a 200
// is hashed and compared; a server with no validators is compared by its body's hash; a
// first check (nothing known) is a change. A refused fetch is refused as a compile's is.
func TestCheck(t *testing.T) {
	body := "ads.example.com\n"
	lm := "Sun, 01 Mar 2026 12:00:00 GMT"
	validators := true
	var got []string
	srv, f := serveTLS(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.Header.Get("If-None-Match")+"|"+r.Header.Get("If-Modified-Since"))
		if validators {
			w.Header().Set("ETag", `"v1"`)
			w.Header().Set("Last-Modified", lm)
			if r.Header.Get("If-None-Match") == `"v1"` {
				w.WriteHeader(http.StatusNotModified)
				return
			}
		}
		w.Write([]byte(body))
	}))
	ctx := context.Background()
	first, same, err := Check(ctx, f, nil, srv.URL+"/l.txt", nil)
	if err != nil || same || first.SHA256 == "" || first.ETag != `"v1"` || first.LastModified != lm || got[0] != "|" {
		t.Fatalf("first: %+v %v %v %v", first, same, err, got)
	}
	again, same, err := Check(ctx, f, nil, srv.URL+"/l.txt", &first)
	if err != nil || !same || again != first || got[1] != `"v1"|`+lm {
		t.Fatalf("304: %+v %v %v %v", again, same, err, got)
	}
	// No validators: the body, by its hash.
	validators = false
	plain, same, err := Check(ctx, f, nil, srv.URL+"/l.txt", &first)
	if err != nil || !same || plain.SHA256 != first.SHA256 || plain.ETag != "" {
		t.Fatalf("same body: %+v %v %v", plain, same, err)
	}
	body = "ads.example.com\ntracker.example.com\n"
	changed, same, err := Check(ctx, f, nil, srv.URL+"/l.txt", &plain)
	if err != nil || same || changed.SHA256 == plain.SHA256 {
		t.Fatalf("changed: %+v %v %v", changed, same, err)
	}
	// The compile's hash of the same body is the check's.
	res, err := Run(ctx, Request{Sources: []Source{domains(srv.URL + "/l.txt")}, Fetch: f})
	if err != nil || res.Sources[0].Fetched == nil || res.Sources[0].Fetched.SHA256 != changed.SHA256 {
		t.Fatalf("compile: %+v %v", res, err)
	}
	// Plain http, not on the internal allowlist: refused before anything is sent.
	if _, _, err := Check(ctx, f, nil, "http://lists.example/l.txt", nil); err == nil || !strings.Contains(err.Error(), "plain http") {
		t.Errorf("plain http: %v", err)
	}
}
