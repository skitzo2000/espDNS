package boards

import (
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/skitzo2000/espdns/controller/internal/memplan"
)

// Chip is what an image's chip allows: its pins, which of them are taken or need care, and
// which peripherals it has.
type Chip struct {
	Name     string
	GPIOs    int            // GPIO 0 .. GPIOs-1
	Missing  []int          // numbers in that range with no pin
	Reserved map[int]string // never usable (flash, PSRAM): an error
	Caution  map[int]string // usable with care (strapping, USB, console): a warning
	EMAC     bool
	WiFi     bool
}

func span(from, to int, why string, m map[int]string) map[int]string {
	for i := from; i <= to; i++ {
		m[i] = why
	}
	return m
}

func with(m map[int]string, why string, pins ...int) map[int]string {
	for _, p := range pins {
		m[p] = why
	}
	return m
}

var (
	chipS3 = Chip{Name: "esp32s3", GPIOs: 49, Missing: []int{22, 23, 24, 25},
		Reserved: span(26, 32, "connected to the flash and PSRAM", map[int]string{}),
		Caution: with(with(with(map[int]string{}, "a strapping pin: boot mode depends on its level at reset", 0, 3, 45, 46),
			"the USB port (logs and Improv)", 19, 20), "the serial console (UART0)", 43, 44),
		WiFi: true}
	chipP4 = Chip{Name: "esp32p4", GPIOs: 55, Reserved: map[int]string{},
		Caution: with(with(map[int]string{}, "a strapping pin: boot mode depends on its level at reset", 34, 35, 36, 37, 38),
			"the USB port (logs)", 24, 25),
		EMAC: true}
)

// Images are the chip images (firmware/images) and their chips.
var Images = map[string]Chip{
	"esp32p4-rev1":  chipP4,
	"esp32p4":       chipP4,
	"esp32s3-octal": withReserved(chipS3, 33, 37, "connected to the octal PSRAM"),
	"esp32s3-eth":   withReserved(chipS3, 33, 37, "connected to the octal PSRAM"),
	"esp32s3-quad":  chipS3,
}

// Siblings are chip images of the same chip, PSRAM and flash layout, built differently
// (esp32s3-eth is esp32s3-octal with speed bought with internal RAM, for wired boards):
// a node moves between them over the air. Each takes the other's board definition and
// signed releases (firmware/CMakeLists.txt has the same pairs).
var Siblings = map[string]string{
	"esp32s3-octal": "esp32s3-eth",
	"esp32s3-eth":   "esp32s3-octal",
}

// Swappable says whether a node running chip image from may be moved to chip image to over the
// air: the same image, or its sibling.
func Swappable(from, to string) bool {
	return from != "" && (from == to || Siblings[from] == to)
}

func withReserved(c Chip, from, to int, why string) Chip {
	r := map[int]string{}
	for k, v := range c.Reserved {
		r[k] = v
	}
	c.Reserved = span(from, to, why, r)
	return c
}

// pinUse is one pin a board uses.
type pinUse struct {
	what string
	pin  int
}

func pins(b Board) []pinUse {
	var u []pinUse
	add := func(what string, p *int) {
		if p != nil {
			u = append(u, pinUse{what, *p})
		}
	}
	for _, s := range b.SPI {
		add(fmt.Sprintf("SPI%d SCLK", s.Host), s.SCLK)
		add(fmt.Sprintf("SPI%d MOSI", s.Host), s.MOSI)
		add(fmt.Sprintf("SPI%d MISO", s.Host), s.MISO)
	}
	if e := b.Ethernet; e != nil {
		switch e.Kind {
		case "emac":
			add("Ethernet PHY reset", e.Reset)
			add("Ethernet power", e.Power)
			add("Ethernet MDC", e.MDC)
			add("Ethernet MDIO", e.MDIO)
			if e.RMIIClock != nil {
				add("Ethernet RMII clock", e.RMIIClock.GPIO)
			}
		case "w5500":
			add("Ethernet CS", e.CS)
			add("Ethernet INT", e.Int)
			add("Ethernet RST", e.Rst)
		}
	}
	if b.SD != nil && b.SD.Kind == "spi" {
		add("SD CS", b.SD.CS)
	}
	if b.LED != nil {
		for _, p := range b.LED.Pins() {
			add(strings.TrimSuffix("LED "+p.Key, " pin"), p.Pin)
		}
	}
	return u
}

// CheckLED is what is wrong with l as the status LED of board b, in place of b's own (a node
// config's "led"): Check's errors about it, so its pins are checked against the chip's and the
// board's others, and a pin Check only warns about (strapping, USB, console) unless b's own
// LED is on it. nil if nothing; b is a board that passes Check.
func CheckLED(b Board, l LED) error {
	var own []int
	if b.LED != nil {
		for _, p := range b.LED.Pins() {
			if p.Pin != nil {
				own = append(own, *p.Pin)
			}
		}
	}
	b.LED = &l
	errs, _ := Check(b)
	errs = slices.DeleteFunc(errs, func(s string) bool { return !strings.Contains(s, "LED") && !strings.HasPrefix(s, "led") })
	if len(errs) == 0 {
		for _, u := range pins(Board{LED: &l}) {
			if why := Images[b.Image].Caution[u.pin]; why != "" && !slices.Contains(own, u.pin) {
				errs = append(errs, fmt.Sprintf("%s: GPIO %d is %s", u.what, u.pin, why))
			}
		}
	}
	if len(errs) == 0 {
		return nil
	}
	return errors.New(strings.Join(errs, "; "))
}

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

// Check returns what is wrong with a board (errors: the firmware or the chip would refuse it)
// and what deserves a second look (warnings). The firmware's own checks
// (firmware/main/board_def.c) are a subset of these.
func Check(b Board) (errs, warns []string) {
	errs, warns = []string{}, []string{} // lists, never null, in JSON
	e := func(f string, a ...any) { errs = append(errs, fmt.Sprintf(f, a...)) }
	w := func(f string, a ...any) { warns = append(warns, fmt.Sprintf(f, a...)) }

	if b.Name == "" || len(b.Name) > 23 {
		e("name: 1-23 characters")
	} else if strings.ContainsAny(b.Name, "/\\ ") {
		e("name: no spaces or slashes")
	}
	chip, ok := Images[b.Image]
	if !ok {
		e("image %q: not one of the chip images", b.Image)
		return errs, warns
	}
	if b.FlashMB != 0 && !oneOf(fmt.Sprint(b.FlashMB), "4", "8", "16", "32") {
		e("flash_mb: 4, 8, 16 or 32")
	}
	if b.FlashMB != 0 && b.FlashMB < 4 {
		e("flash_mb: at least 4 MB is needed for two app slots")
	}
	if b.Tier != "" && !oneOf(b.Tier, "tested", "community", "untested") {
		e("tier: tested, community or untested")
	}
	if b.Network != nil {
		if err := b.Network.Check(); err != nil {
			e("%v", err)
		}
	}

	buses := map[int]bool{}
	if len(b.SPI) > 2 {
		e("spi: at most 2 buses")
	}
	for _, s := range b.SPI {
		if s.Host != 2 && s.Host != 3 {
			e("spi host %d: 2 or 3", s.Host)
		}
		if buses[s.Host] {
			e("spi host %d defined twice", s.Host)
		}
		buses[s.Host] = true
		if s.SCLK == nil || s.MOSI == nil || s.MISO == nil {
			e("spi host %d: sclk, mosi and miso are required", s.Host)
		}
	}

	if x := b.Ethernet; x != nil {
		switch x.Kind {
		case "none":
		case "emac":
			if !chip.EMAC {
				e("ethernet: an %s has no built-in Ethernet MAC: use an SPI chip (w5500)", chip.Name)
			}
			if x.PHY != "" && !oneOf(x.PHY, "ip101", "lan87xx", "rtl8201", "dp83848") {
				e("ethernet phy %q: ip101, lan87xx, rtl8201 or dp83848", x.PHY)
			}
			if x.Addr != nil && (*x.Addr < -1 || *x.Addr > 31) {
				e("ethernet addr: 0-31, or -1 to search")
			}
			if x.RMIIClock != nil {
				if !oneOf(x.RMIIClock.Mode, "default", "in", "out") {
					e("ethernet rmii_clock mode: in or out")
				}
				if x.RMIIClock.Mode != "default" && x.RMIIClock.GPIO == nil {
					e("ethernet rmii_clock: gpio is required with a mode")
				}
			}
		case "w5500":
			if !buses[x.SPIHost] {
				e("ethernet spi_host %d: no such SPI bus on this board", x.SPIHost)
			}
			if x.CS == nil {
				e("ethernet cs: required")
			}
			if x.MHz != 0 && (x.MHz < 1 || x.MHz > 80) {
				e("ethernet mhz: 1-80")
			}
			if x.Int == nil {
				w("ethernet int: not connected, so the W5500 is polled every 10 ms")
			}
		default:
			e("ethernet kind %q: none, emac or w5500", x.Kind)
		}
	}
	if (b.Ethernet == nil || b.Ethernet.Kind == "none" || b.Ethernet.Optional) && !chip.WiFi {
		e("no network: an %s has no Wi-Fi, so it needs Ethernet that is always fitted", chip.Name)
	} else if b.Ethernet == nil || b.Ethernet.Kind == "none" {
		w("no Ethernet: this node runs on Wi-Fi, which is best effort")
	}

	if s := b.SD; s != nil {
		switch s.Kind {
		case "none":
		case "sdmmc":
			if s.Width != 0 && s.Width != 1 && s.Width != 4 {
				e("sd width: 1 or 4")
			}
		case "spi":
			if !buses[s.SPIHost] {
				e("sd spi_host %d: no such SPI bus on this board", s.SPIHost)
			}
			if s.CS == nil {
				e("sd cs: required")
			}
		default:
			e("sd kind %q: none, sdmmc or spi", s.Kind)
		}
	}
	if b.SD == nil || b.SD.Kind == "none" {
		w("no SD card: zones and blocking live in RAM only")
	}
	if b.LED != nil {
		if err := b.LED.Check(); err != nil {
			e("%v", err)
		}
	}
	if b.WiFi != nil && b.WiFi.TxPowerDBm != 0 {
		if !chip.WiFi {
			e("wifi: an %s has no Wi-Fi", chip.Name)
		} else if b.WiFi.TxPowerDBm < 2 || b.WiFi.TxPowerDBm > 20 {
			e("wifi tx_power_dbm: 2-20, or leave it out for the chip's 20 dBm")
		}
	}

	// PSRAM is required: without it the DNS service's buffers don't fit internal RAM, and
	// 8 MB is the smallest size tested (docs/hardware.md, What a board needs).
	if b.PSRAMMB != nil && *b.PSRAMMB > 64 {
		e("psram_mb: 8-64")
	} else if b.PSRAMMB != nil && *b.PSRAMMB < 8 {
		e("psram_mb: espDNS needs at least 8 MB of PSRAM")
	}
	// The memory plan's values (internal/memplan), as the firmware checks them.
	if m := b.Memory; m != nil {
		for _, f := range []struct {
			key    string
			v      *int
			lo, hi int
		}{
			{"internal_kb", m.InternalKB, 16, 65536},
			{"cache_kb", m.CacheKB, 16, 65536},
			{"cache_entries", m.CacheEntries, 16, 1048576},
			{"blocklist_kb", m.BlocklistKB, 16, 65536},
			{"blocklist_index_kb", m.BlocklistIndexKB, 0, 65536},
			{"hosted_zones_kb", m.HostedZonesKB, 16, 65536},
			{"secondary_zones_kb", m.SecondaryZonesKB, 16, 65536},
			{"querylog_kb", m.QueryLogKB, 0, 65536},
			{"fwd_pending", m.FwdPending, 2, 1024},
		} {
			if f.v != nil && (*f.v < f.lo || *f.v > f.hi) {
				e("memory %s: %d-%d, or leave it out for the chip image's default", f.key, f.lo, f.hi)
			}
		}
	}
	if err := checkCPU(b); err != nil {
		e("%v", err)
	}
	// Every service at once: a board that can't hold them all still runs a config with fewer,
	// which the node checks (and the controller, before it pushes one).
	// Without psram_mb the node plans with the PSRAM its chip finds, which isn't known here.
	if len(errs) == 0 && b.PSRAMMB != nil {
		if _, err := memplan.Make(memplan.Values(b.MemoryKeys(), b.Image, -1), memplan.Services); err != nil {
			w("memory: every service at once doesn't fit (%v): a node config must turn some off", err)
		}
	}

	// Pins: each used once, and allowed on this chip.
	used := map[int]string{}
	missing := map[int]bool{}
	for _, m := range chip.Missing {
		missing[m] = true
	}
	uses := pins(b)
	sort.SliceStable(uses, func(i, j int) bool { return uses[i].pin < uses[j].pin })
	for _, u := range uses {
		switch {
		case u.pin < 0 || u.pin >= chip.GPIOs || missing[u.pin]:
			e("%s: GPIO %d doesn't exist on an %s", u.what, u.pin, chip.Name)
			continue
		case chip.Reserved[u.pin] != "":
			e("%s: GPIO %d is %s", u.what, u.pin, chip.Reserved[u.pin])
		case chip.Caution[u.pin] != "":
			w("%s: GPIO %d is %s", u.what, u.pin, chip.Caution[u.pin])
		}
		if prev, dup := used[u.pin]; dup {
			e("GPIO %d used twice: %s and %s", u.pin, prev, u.what)
		}
		used[u.pin] = u.what
	}
	return errs, warns
}
