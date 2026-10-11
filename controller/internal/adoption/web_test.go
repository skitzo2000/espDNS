package adoption

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/skitzo2000/espdns/controller/internal/configs"
)

// far is a config made for another subnet: its static address and gateway are another
// network's (the node to adopt is on 203.0.113.0/24).
const far = `{"name": "dns2", "network": {"address": "192.0.2.253/23", "gateway": "192.0.2.1"}}`

// web is + Add node's request for the node to adopt, with far.json, as built.
func (e *env) web(w Web) (Request, error) {
	w.Node, w.NodeID, w.Config, w.Static = "203.0.113.52", e.node.ID(), "far.json", true
	r, err := w.Request(e.dir, "")
	r.Wait = 5 * time.Second
	return r, err
}

// DHCP, reserved for the node in the router, from + Add node: let through with "reserved",
// and the node ends up on DHCP, at the address it has, with a config that says dhcp and no
// gateway: not the config's static address.
func TestWebDHCPReserved(t *testing.T) {
	e := newEnv(t)
	e.config("far.json", far)
	r, err := e.web(Web{Address: "dhcp", Reserved: true})
	if err != nil {
		t.Fatal(err)
	}
	if r.Address != "dhcp" || !r.Reserved || r.Gateway != "" {
		t.Fatalf("%+v", r)
	}
	ctx := context.Background()
	b, err := Build(ctx, e.client(), r, e.logf)
	if err != nil {
		t.Fatal(err)
	}
	if !b.Adopt.DHCP || !b.Adopt.Reserved || b.Adopt.Address.IsValid() || b.Adopt.Gateway.IsValid() {
		t.Fatalf("%+v", b.Adopt)
	}
	res, err := Run(ctx, e.client(), b, "test", e.logf)
	if err != nil {
		t.Fatal(err, e.log())
	}
	p, err := configs.LoadPushed(e.dir, e.node.ID())
	if err != nil || p == nil || res.Addr != "203.0.113.52" || len(e.pushes(e.node)) != 1 {
		t.Fatalf("%+v %+v %v %v", res, p, err, e.node.Events())
	}
	pl := string(p.Payload)
	if !strings.Contains(pl, `"network":{"address":"dhcp"}`) || strings.Contains(pl, "gateway") || strings.Contains(pl, "192.0.2.") {
		t.Fatal(pl)
	}
}

// dhcp from + Add node without "reserved" is still refused, as an empty address is; a fixed
// address needs its gateway, one in its own network.
func TestWebStaticRefuses(t *testing.T) {
	e := newEnv(t)
	e.config("far.json", far)
	for _, tc := range []struct {
		w    Web
		want string
	}{
		{Web{Address: "dhcp"}, `"address": dhcp only once the node's address is reserved`},
		{Web{}, "a node added here gets a fixed address"},
		{Web{Address: "203.0.113.60/24"}, `"gateway": give the gateway for 203.0.113.60/24`},
		{Web{Address: "203.0.113.60/24", Gateway: "192.0.2.1"}, "192.0.2.1 is not in 203.0.113.0/24, the node's own network"},
		{Web{Address: "203.0.113.60/24", Gateway: "203.0.113.60"}, "another address in the node's network"},
		{Web{Address: "dhcp", Reserved: true, Gateway: "203.0.113.1"}, "only with a static address"},
	} {
		if _, err := e.web(tc.w); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%+v: %v, want %q", tc.w, err, tc.want)
		}
	}
}

// A config with another subnet's address and gateway, given a fixed address and gateway on
// the node's own: builds, with the gateway given (never the config's), and the node runs
// what was chosen.
func TestWebFixedOverOtherSubnetConfig(t *testing.T) {
	e := newEnv(t)
	e.config("far.json", far)
	r, err := e.web(Web{Address: "203.0.113.60/24", Gateway: "203.0.113.1"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	b, err := Build(ctx, e.client(), r, e.logf)
	if err != nil {
		t.Fatal(err)
	}
	if b.Adopt.Address.String() != "203.0.113.60/24" || b.Adopt.Gateway.String() != "203.0.113.1" || b.Adopt.DHCP {
		t.Fatalf("%+v", b.Adopt)
	}
	res, err := Run(ctx, e.client(), b, "test", e.logf)
	if err != nil || res.Addr != "203.0.113.60" {
		t.Fatal(res, err, e.log())
	}
	p, _ := configs.LoadPushed(e.dir, e.node.ID())
	if p == nil || !strings.Contains(string(p.Payload), `"network":{"address":"203.0.113.60/24","gateway":"203.0.113.1"}`) {
		t.Fatal(p)
	}
}

// The gateway a request gives is the one used, never the config's (here another network's);
// without one, the CLI's request still falls back to the config's.
func TestRequestGatewayOverConfigs(t *testing.T) {
	e := newEnv(t)
	e.config("far.json", far)
	ctx := context.Background()
	b, err := Build(ctx, e.client(), e.req(Request{Config: "far.json", Address: "203.0.113.60/24", Gateway: "203.0.113.1", DryRun: true}),
		e.logf)
	if err != nil {
		t.Fatal(err)
	}
	if b.Adopt.Gateway.String() != "203.0.113.1" {
		t.Fatal(b.Adopt.Gateway)
	}
	b, err = Build(ctx, e.client(), e.req(Request{Config: "far.json", Address: "203.0.113.60/24", DryRun: true}), e.logf)
	if err != nil {
		t.Fatal(err)
	}
	if b.Adopt.Gateway.String() != "192.0.2.1" {
		t.Fatal(b.Adopt.Gateway)
	}
}
