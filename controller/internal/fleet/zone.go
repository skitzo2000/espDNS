package fleet

// A node's copy of a secondary zone (GET /zone, docs/reference/node-endpoints.md#zone): its
// records as it answers from them, read a page at a time. The Zones page lists them.

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"

	"github.com/miekg/dns"
)

// errChanged is a /zone page refused because the zone changed since the read began (409).
var errChanged = errors.New("the zone changed during the read")

// zoneTries are the reads of a zone that keeps changing under the reader before giving up;
// zoneMaxRecords and zoneMaxBytes bound one read (a node holds far fewer, in far less).
const (
	zoneTries      = 3
	zoneMaxRecords = 20000
	zoneMaxBytes   = 32 << 20
)

// Zone is a secondary zone as a node holds it.
type Zone struct {
	Name   string
	Serial uint32
	RRs    []dns.RR // in the zone's canonical order
}

// ReadZone reads the node's copy of the zone whole, starting again if it changes on the node
// mid-read (at most zoneTries times).
func (c *Client) ReadZone(ctx context.Context, host, name string) (Zone, error) {
	for try := 1; ; try++ {
		z, err := c.readZone(ctx, host, name)
		if !errors.Is(err, errChanged) || try == zoneTries {
			return z, err
		}
	}
}

// readZone reads every page of one generation of the zone.
func (c *Client) readZone(ctx context.Context, host, name string) (Zone, error) {
	var z Zone
	q := url.Values{"name": {name}}
	read := 0
	for from := uint64(0); ; {
		q.Set("from", strconv.FormatUint(from, 10))
		b, err := c.fetch(ctx, host, "/zone?"+q.Encode())
		if errors.Is(err, ErrNotSupported) {
			return z, fmt.Errorf("%s holds no copy of %s", host, name)
		}
		if err != nil {
			return z, err
		}
		if read += len(b); read > zoneMaxBytes {
			return z, fmt.Errorf("%s/zone: over %d bytes", host, zoneMaxBytes)
		}
		var p struct {
			Name   string `json:"name"`
			Serial uint32 `json:"serial"`
			Gen    uint64 `json:"gen"`
			RRs    []struct {
				O   string `json:"o"`
				T   uint16 `json:"t"`
				TTL uint32 `json:"ttl"`
				D   string `json:"d"`
			} `json:"rrs"`
			Next uint64 `json:"next"`
			More bool   `json:"more"`
		}
		if err := json.Unmarshal(b, &p); err != nil {
			return z, fmt.Errorf("%s/zone: %w", host, err)
		}
		if from == 0 {
			z.Name, z.Serial = p.Name, p.Serial
			q.Set("gen", strconv.FormatUint(p.Gen, 10))
		}
		if len(z.RRs)+len(p.RRs) > zoneMaxRecords {
			return z, fmt.Errorf("%s/zone: over %d records", host, zoneMaxRecords)
		}
		for _, r := range p.RRs {
			d, err := hex.DecodeString(r.D)
			if err != nil || len(d) > 0xffff {
				return z, fmt.Errorf("%s/zone: %s: bad rdata", host, r.O)
			}
			h := dns.RR_Header{Name: dns.Fqdn(r.O), Rrtype: r.T, Class: dns.ClassINET, Ttl: r.TTL, Rdlength: uint16(len(d))}
			rr, _, err := dns.UnpackRRWithHeader(h, d, 0)
			if err != nil {
				return z, fmt.Errorf("%s/zone: %s: %w", host, r.O, err)
			}
			z.RRs = append(z.RRs, rr)
		}
		if !p.More {
			return z, nil
		}
		if p.Next <= from {
			return z, fmt.Errorf("%s/zone: no records after %d", host, from)
		}
		from = p.Next
	}
}
