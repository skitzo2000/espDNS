package fleet_test

import (
	"context"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/miekg/dns"

	"github.com/skitzo2000/espdns/controller/internal/fakenode"
	"github.com/skitzo2000/espdns/controller/internal/fleet"
)

// testZone is a zone of n address records after its SOA (serial).
func testZone(t *testing.T, serial uint32, n int) []dns.RR {
	t.Helper()
	rrs := []dns.RR{}
	for i, s := range append([]string{fmt.Sprintf("lab.example. 3600 IN SOA ns1.lab.example. hostmaster.lab.example. %d 3600 600 604800 300", serial),
		`lab.example. 300 IN TXT "hello there"`, "lab.example. 300 IN MX 10 mail.lab.example."}, make([]string, n)...) {
		if s == "" {
			s = fmt.Sprintf("h%d.lab.example. 300 IN A 192.0.2.%d", i, i%250)
		}
		rr, err := dns.NewRR(s)
		if err != nil {
			t.Fatal(err)
		}
		rrs = append(rrs, rr)
	}
	return rrs
}

// A node's copy of a zone read whole, a page at a time; read again from the start when it
// changes mid-read; given up on when it keeps changing; an error from a node without it.
func TestReadZone(t *testing.T) {
	n := fakenode.New("dns-a", [6]byte{2, 0, 0, 0, 0, 1}, "esp32p4-rev1", "p4-ip101", nil)
	n.SetCopy("lab.example", testZone(t, 7, 450))
	pages := 0
	n.ZonePage = func(*fakenode.Node) { pages++ }
	srv := httptest.NewServer(n)
	defer srv.Close()
	host := strings.TrimPrefix(srv.URL, "http://")
	c := &fleet.Client{}
	ctx := context.Background()

	z, err := c.ReadZone(ctx, host, "lab.example")
	if err != nil {
		t.Fatal(err)
	}
	want := testZone(t, 7, 450)
	if z.Name != "lab.example" || z.Serial != 7 || len(z.RRs) != len(want) || pages != 3 {
		t.Fatalf("read %s %d, %d records in %d pages", z.Name, z.Serial, len(z.RRs), pages)
	}
	for i := range want {
		if z.RRs[i].String() != want[i].String() {
			t.Fatalf("record %d: %s, want %s", i, z.RRs[i], want[i])
		}
	}

	// Swapped after the first page: the read starts again and returns the new copy.
	swaps := 1
	n.Do(func(n *fakenode.Node) {
		n.ZonePage = func(n *fakenode.Node) {
			if swaps > 0 {
				swaps--
				n.SetCopy("lab.example", testZone(t, 8, 300))
			}
		}
	})
	if z, err = c.ReadZone(ctx, host, "lab.example"); err != nil || z.Serial != 8 || len(z.RRs) != 303 {
		t.Fatalf("after a swap: %d, %d records, %v", z.Serial, len(z.RRs), err)
	}
	// Swapped on every page: given up on.
	n.Do(func(n *fakenode.Node) {
		n.ZonePage = func(n *fakenode.Node) { n.SetCopy("lab.example", testZone(t, 9, 300)) }
	})
	if _, err = c.ReadZone(ctx, host, "lab.example"); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Errorf("always changing: %v", err)
	}
	if _, err = c.ReadZone(ctx, host, "other.example"); err == nil || !strings.Contains(err.Error(), "no copy") {
		t.Errorf("a zone it doesn't hold: %v", err)
	}
}
