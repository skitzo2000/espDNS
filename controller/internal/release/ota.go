package release

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/skitzo2000/espdns/controller/internal/boards"
)

// AppDesc is what an ESP-IDF app image says about itself (esp_app_desc_t), as
// firmware/tools/ota_push.py reads it.
type AppDesc struct {
	Project   string
	Version   string
	Built     string
	ElfSHA256 string // the first 8 bytes, hex: what /status reports as elf_sha256
}

// appDescOff is where esp_app_desc_t starts: esp_image_header_t (24) and the first
// segment's header (8).
const appDescOff = 32

// ParseAppDesc reads the app descriptor of a firmware image (an app .bin).
func ParseAppDesc(image []byte) (AppDesc, error) {
	if len(image) < appDescOff+256 || image[0] != 0xE9 {
		return AppDesc{}, errors.New("not an ESP app image")
	}
	d := image[appDescOff:]
	if binary.LittleEndian.Uint32(d) != 0xABCD5432 {
		return AppDesc{}, errors.New("image has no app descriptor")
	}
	str := func(off, n int) string {
		b, _, _ := bytes.Cut(d[off:off+n], []byte{0})
		return string(b)
	}
	return AppDesc{
		Version:   str(16, 32),
		Project:   str(48, 32),
		Built:     str(96, 16) + " " + str(80, 16),
		ElfSHA256: hex.EncodeToString(d[144:152]),
	}, nil
}

// builtinAddrMark is what precedes the address built into a firmware image (the firmware's
// CFG_BUILTIN_ADDR_MARK, main/cfg.h), NUL-terminated; "" for none.
const builtinAddrMark = "espdns:builtin-address="

// BuiltinAddress is the IPv4 address built into a firmware image (make STATIC_IP=..., a
// transitional image's), "" for none. known is false for an image from before the firmware
// marked it, which says nothing either way.
func BuiltinAddress(image []byte) (addr string, known bool) {
	i := bytes.Index(image, []byte(builtinAddrMark))
	if i < 0 {
		return "", false
	}
	rest := image[i+len(builtinAddrMark):]
	end := bytes.IndexByte(rest, 0)
	if end < 0 || end > len("255.255.255.255") {
		return "", false
	}
	return string(rest[:end]), true
}

// The markers a firmware image carries about its keys (the firmware's KEYS_MARK and
// KEYS_SEED_MARK, main/keys_node.h), NUL-terminated.
const (
	keysPartitionMark = "espdns:keys=partition-v1\x00"
	keysSeedMark      = "espdns:keys-seed="
)

// ImageKeys is what a firmware image says of the keys it trusts.
type ImageKeys struct {
	// Partition: it reads its keys from the keys partition (firmware from before has no
	// marker, and trusts the keys built into it).
	Partition bool
	// Seed is the fingerprints of the keys built in as the partition's seed (a KEYS_SEED=
	// build), release then recovery; nil for a key-neutral image.
	Seed []string
}

// KeysOf reads an image's keys markers; an error for a seed marker it can't read.
func KeysOf(image []byte) (ImageKeys, error) {
	k := ImageKeys{Partition: bytes.Contains(image, []byte(keysPartitionMark))}
	i := bytes.Index(image, []byte(keysSeedMark))
	if i < 0 {
		return k, nil
	}
	rest := image[i+len(keysSeedMark):]
	end := bytes.IndexByte(rest, 0)
	if end < 0 || end > 2*16+1 {
		return k, errors.New("unreadable keys seed marker")
	}
	fps := strings.Split(string(rest[:end]), ",")
	if len(fps) != 2 {
		return k, errors.New("keys seed marker without two fingerprints")
	}
	for _, f := range fps {
		if b, err := hex.DecodeString(f); err != nil || len(b) != 8 {
			return k, fmt.Errorf("keys seed marker: bad fingerprint %q", f)
		}
	}
	k.Seed = fps
	return k, nil
}

// FirmwareKeysCheck refuses firmware app for the node at host, whose /status is st, that
// would leave it without the keys it trusts (docs/architecture.md, Signed releases and pins):
//   - an app without the keys partition marker (espdns:keys=partition-v1) is firmware from
//     before key-neutral images, with keys built in: pushing it is a downgrade, refused;
//   - a node in setup mode (keys_source "none" or "corrupt") takes nothing over the network
//     until its keys are written over USB;
//   - a node on firmware from before the keys partition (no keys_source in /status), or one
//     whose partition can't be used ("embedded"), would come up on the new firmware with no
//     keys unless the app carries a seed of the keys it has now: only a build with a
//     keys-seed whose fingerprints are its keys (release, recovery) is taken.
func FirmwareKeysCheck(host string, st NodeStatus, app []byte) error {
	k, err := KeysOf(app)
	if err != nil {
		return fmt.Errorf("this firmware's keys marker: %w", err)
	}
	if !k.Partition {
		return fmt.Errorf("this firmware doesn't read the keys partition (no espdns:keys=partition-v1 marker): it is a build "+
			"from before key-neutral images, with keys built in, and pushing it to %s would be a downgrade: refused", host)
	}
	switch st.KeysSource {
	case "partition", "seeded":
		return nil
	case "none", "corrupt":
		return fmt.Errorf("%s has no keys (setup mode: keys_source %s), so it takes nothing over the network: reinstall it "+
			"over USB, or write its keys with Install over USB, Write keys only", host, st.KeysSource)
	}
	what := "runs firmware from before the keys partition"
	if st.KeysSource != "" {
		what = "can't use its keys partition (keys_source " + st.KeysSource + ")"
	}
	if len(st.Keys) < 2 {
		return fmt.Errorf("%s %s and reports no keys: refused", host, what)
	}
	if k.Seed == nil {
		return fmt.Errorf("%s %s, and this firmware is key-neutral: it would come up with no keys. Push it a build with "+
			"its keys as the seed (make -C controller fleet-firmware with FLEET_KEYS_SEED=<its public keys>) first", host, what)
	}
	if k.Seed[0] != st.Keys[0] || k.Seed[1] != st.Keys[1] {
		return fmt.Errorf("%s %s, and this firmware's keys seed (release %s, recovery %s) is not its keys (release %s, "+
			"recovery %s): it would trust other keys. Build it with its own public keys as the seed", host, what,
			k.Seed[0], k.Seed[1], st.Keys[0], st.Keys[1])
	}
	return nil
}

// FirmwareOptions say how a firmware release is signed and applied.
type FirmwareOptions struct {
	// Image is the chip image the app was built as (esp32s3-octal, ...); the node must run it
	// or its sibling (boards.Siblings).
	Image string
	// Board is, for a node on firmware from before board definitions, the board the
	// transitional image was built for: those nodes check the board name instead.
	Board string
	// Later asks the node to stage the image and wait for a reboot command
	// (X-OTA-Reboot: later) instead of rebooting into it straight away. Firmware that
	// doesn't know the header reboots straight away.
	Later bool
	// Check, if set, checks the image right before it is signed (dist.CheckProvenance: the
	// imported release it came from still vouches for it); an error and nothing is pushed.
	Check func(image []byte) error
}

// PushFirmware signs image as a firmware release for the node at host and posts it to
// /ota, as tools/ota_push.py does. It returns once the node took the image, before it runs
// it: internal/fleet waits for the node to confirm it, or roll it back. Before it signs:
// the node must trust the key, the image must keep the node's keys (FirmwareKeysCheck, on
// the /status read now), and o.Check must pass.
func (p *Pusher) PushFirmware(ctx context.Context, host string, image []byte, o FirmwareOptions) (Pushed, error) {
	var out Pushed
	if _, err := ParseAppDesc(image); err != nil {
		return out, err
	}
	st, err := p.Status(ctx, host)
	if err != nil {
		return out, err
	}
	out.Before = st
	if err := p.trusted(host, st); err != nil {
		return out, err
	}
	if err := FirmwareKeysCheck(host, st, image); err != nil {
		return out, err
	}
	// The name the node checks: its chip image; or, on firmware from before board
	// definitions, the board it was built for (only a transitional image for it fits).
	// A sibling image's app (boards.Siblings) is signed for the image the node runs, the
	// name it checks: that moves it to the sibling.
	name := o.Image
	if st.Image != "" {
		if !boards.Swappable(st.Image, o.Image) {
			return out, fmt.Errorf("%s runs chip image %s, this is %s", host, st.Image, o.Image)
		}
		name = st.Image
	} else {
		if o.Board == "" || st.Board != o.Board {
			return out, fmt.Errorf("%s runs firmware from before board definitions, built for board %s: "+
				"push a transitional image for it (BOARD=%s)", host, st.Board, st.Board)
		}
		name = o.Board
	}
	if o.Check != nil {
		if err := o.Check(image); err != nil {
			return out, err
		}
	}
	node, seq, err := p.target(host, Firmware, st)
	if err != nil {
		return out, err
	}
	hdr, err := Header(p.KeyFor(st), Firmware, p.KeyID, node, name, seq, image)
	if err != nil {
		return out, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://"+host+"/ota",
		bytes.NewReader(append(hdr, image...)))
	if err != nil {
		return out, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	if o.Later {
		req.Header.Set("X-OTA-Reboot", "later")
	}
	resp, err := p.client().Do(req)
	if err != nil {
		return out, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	reply := strings.TrimSpace(string(body))
	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("%s firmware seq %d rejected: %s %s", host, seq, resp.Status, reply)
	}
	out.Seq = seq
	out.Node = ParseReply(reply)
	out.Reply = fmt.Sprintf("firmware seq %d: %s", seq, out.Node.line())
	return out, nil
}
