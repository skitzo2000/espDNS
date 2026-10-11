package blocklist

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"net/http"
)

// Fetched is what a URL source's server sent: the SHA-256 of the body, and the validators
// it gave for it (ETag, Last-Modified; "" none), which the next conditional GET sends back.
type Fetched struct {
	SHA256       string `json:"sha256"`
	ETag         string `json:"etag,omitempty"`
	LastModified string `json:"last_modified,omitempty"`
}

func fetchedOf(resp *http.Response, h hash.Hash) *Fetched {
	return &Fetched{SHA256: hex.EncodeToString(h.Sum(nil)), ETag: resp.Header.Get("ETag"), LastModified: resp.Header.Get("Last-Modified")}
}

// Check asks whether a URL source changed since prev was fetched (prev nil: never), under
// the same rules as a compile's fetch (https, public addresses, the internal allowlist:
// fetch.go). It sends prev's validators (If-None-Match, If-Modified-Since): a 304 is
// unchanged, with no body fetched; otherwise the body is read (at most MaxFetch) and only
// hashed, and it is unchanged when its hash is prev's. It returns what the server has now
// (prev's hash, with any new validators, on a 304).
func Check(ctx context.Context, f *Fetch, internal []string, rawURL string, prev *Fetched) (Fetched, bool, error) {
	hdr := http.Header{}
	if prev != nil && prev.SHA256 != "" {
		if prev.ETag != "" {
			hdr.Set("If-None-Match", prev.ETag)
		}
		if prev.LastModified != "" {
			hdr.Set("If-Modified-Since", prev.LastModified)
		}
	}
	resp, err := f.get(ctx, rawURL, internal, hdr)
	if err != nil {
		return Fetched{}, false, err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotModified && len(hdr) > 0:
		now := *prev
		if e := resp.Header.Get("ETag"); e != "" {
			now.ETag = e
		}
		if lm := resp.Header.Get("Last-Modified"); lm != "" {
			now.LastModified = lm
		}
		return now, true, nil
	case resp.StatusCode != http.StatusOK:
		return Fetched{}, false, fmt.Errorf("HTTP %s", resp.Status)
	}
	lr := &io.LimitedReader{R: resp.Body, N: maxFetch + 1}
	h := sha256.New()
	if _, err := io.Copy(h, lr); err != nil {
		return Fetched{}, false, err
	}
	if lr.N == 0 {
		return Fetched{}, false, fmt.Errorf("more than %d MiB", maxFetch>>20)
	}
	now := fetchedOf(resp, h)
	return *now, prev != nil && prev.SHA256 == now.SHA256, nil
}
