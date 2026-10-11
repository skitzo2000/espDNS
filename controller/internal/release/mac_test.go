package release

import (
	"encoding/json"
	"testing"
)

func TestMAC(t *testing.T) {
	for in, want := range map[string]string{
		"aa:bb:0c:d0:01:ff":       "aa:bb:0c:d0:01:ff",
		"AA:BB:0C:D0:01:FF":       "aa:bb:0c:d0:01:ff", // older firmware's case: shown lower
		"":                        "",
		"00:00:00:00:00:00":       "", // the interface wasn't up
		"aa-bb-0c-d0-01-ff":       "",
		"aabb.0cd0.01ff":          "",
		"aa:bb:0c:d0:01":          "",
		"aa:bb:0c:d0:01:ff:00:11": "",
		"<b>:bb:0c:d0:01:ff":      "",
		"aa:bb:0c:d0:01:fg":       "",
	} {
		if got := MAC(in); got != want {
			t.Errorf("MAC(%q) = %q, want %q", in, got, want)
		}
	}
}

// /status's net.mac is decoded into NodeStatus.
func TestNodeStatusMAC(t *testing.T) {
	var st NodeStatus
	if err := json.Unmarshal([]byte(`{"node_id":"aa:bb:cc:00:00:01","net":{"kind":"ethernet","mac":"aa:bb:cc:00:00:04"}}`), &st); err != nil {
		t.Fatal(err)
	}
	if st.Net.MAC != "aa:bb:cc:00:00:04" || MAC(st.Net.MAC) != "aa:bb:cc:00:00:04" {
		t.Fatalf("net.mac: %q", st.Net.MAC)
	}
}

// /status's keys_source, keys_gen and keys_error (firmware/main/keys_node.h) are decoded into
// NodeStatus; firmware from before the keys partition has none of them.
func TestNodeStatusKeys(t *testing.T) {
	three, one := uint32(3), uint32(1)
	for _, c := range []struct {
		in, source, err string
		gen             *uint32
	}{
		{`{"keys_source":"partition","keys_gen":3,"keys_error":""}`, "partition", "", &three},
		{`{"keys_source":"seeded","keys_gen":1,"keys_error":""}`, "seeded", "", &one},
		{`{"keys_source":"corrupt","keys_gen":null,"keys_error":"no valid slot (slot A: bad CRC)"}`, "corrupt",
			"no valid slot (slot A: bad CRC)", nil},
		{`{"keys_source":"embedded","keys_gen":null,"keys_error":"no keys partition"}`, "embedded", "no keys partition", nil},
		{`{"keys":["aa","bb"]}`, "", "", nil},
	} {
		var st NodeStatus
		if err := json.Unmarshal([]byte(c.in), &st); err != nil {
			t.Fatal(err)
		}
		if st.KeysSource != c.source || st.KeysError != c.err || (st.KeysGen == nil) != (c.gen == nil) ||
			(c.gen != nil && *st.KeysGen != *c.gen) {
			t.Errorf("%s: source %q gen %v error %q", c.in, st.KeysSource, st.KeysGen, st.KeysError)
		}
	}
}
