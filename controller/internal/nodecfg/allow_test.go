package nodecfg

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// "allow": networks the node answers DNS queries from besides the private ones, checked as
// the node checks them (firmware cfg.c, parse_cidr), live; firmware from before it refuses
// the key.
func TestAllow(t *testing.T) {
	c, err := Parse([]byte(`{"allow":["198.51.100.0/24","203.0.113.7/32","2001:db8::/32","::ffff:198.51.100.0/120","0.0.0.0/0"]}`))
	if err != nil {
		t.Fatal(err)
	}
	if p, _ := c.Payload(); !strings.Contains(string(p), `"allow":["198.51.100.0/24",`) || !c.SetsAllow() {
		t.Errorf("payload %s", p)
	}
	if c, err := Parse([]byte(`{"allow":[]}`)); err != nil || !c.SetsAllow() {
		t.Error(err)
	}
	if c, err := Parse([]byte(`{"name":"a"}`)); err != nil || c.SetsAllow() {
		t.Error(err)
	}
	for _, bad := range []string{"198.51.100.0", "198.51.100.1/24", "198.51.100.0/33", "198.51.100.0/", "2001:db8::/129",
		"2001:db8::1/32", "example.com/24", "/24", "1.2.3/24", "fe80::%eth0/64", "198.51.100.0/24 "} {
		if _, err := Parse([]byte(`{"allow":["` + bad + `"]}`)); err == nil || !strings.Contains(err.Error(), "allow:") {
			t.Errorf("%s: %v", bad, err)
		}
	}
	if _, err := Parse([]byte(`{"allow":"198.51.100.0/24"}`)); err == nil {
		t.Error("not a list: taken")
	}
	many := make([]string, MaxAllow+1)
	for i := range many {
		many[i] = fmt.Sprintf(`"198.51.100.%d/32"`, i)
	}
	if _, err := Parse([]byte(`{"allow":[` + strings.Join(many[:MaxAllow], ",") + `]}`)); err != nil {
		t.Errorf("%d networks: %v", MaxAllow, err)
	}
	if _, err := Parse([]byte(`{"allow":[` + strings.Join(many, ",") + `]}`)); err == nil || !strings.Contains(err.Error(), "at most 16") {
		t.Errorf("%d networks: %v", MaxAllow+1, err)
	}
	// Live, as the node applies it (cfg_copy_live).
	from, _ := Parse([]byte(`{}`))
	to, _ := Parse([]byte(`{"allow":["198.51.100.0/24"]}`))
	if ch := Compare(from, to, nil); len(ch.Reboot) != 0 || len(ch.Maybe) != 0 || !slices.Equal(ch.Live, []string{"allow"}) {
		t.Errorf("%+v", ch)
	}
}
