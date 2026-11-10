package boards

import (
	"slices"
	"strings"
	"testing"

	"github.com/skitzo2000/espdns/controller/internal/nodecfg"
)

func p(n int) *int { return &n }

func has(list []string, s string) bool {
	for _, l := range list {
		if strings.Contains(l, s) {
			return true
		}
	}
	return false
}

// The shipped boards pass the checks.
func TestCatalog(t *testing.T) {
	entries, err := Load("../../../boards", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 3 {
		t.Fatalf("%d boards in the catalog", len(entries))
	}
	for _, e := range entries {
		if len(e.Errors) > 0 {
			t.Errorf("%s: %v", e.Board.Name, e.Errors)
		}
		// The library only lists boards espDNS runs on: 8 MB of PSRAM or more, stated.
		if e.Board.PSRAMMB == nil || *e.Board.PSRAMMB < 8 {
			t.Errorf("%s: a catalog board states psram_mb, at least 8", e.Board.Name)
		}
		if e.Board.Tier == "" || e.Board.Title == "" || e.Board.FlashMB == 0 {
			t.Errorf("%s: a catalog board has a title, flash_mb and a tier", e.Board.Name)
		}
	}
}

func TestChecks(t *testing.T) {
	s3 := func() Board {
		return Board{Name: "x", Image: "esp32s3-octal", FlashMB: 8,
			SPI:      []SPIBus{{Host: 2, SCLK: p(7), MOSI: p(9), MISO: p(8)}},
			Ethernet: &Ethernet{Kind: "w5500", SPIHost: 2, CS: p(1), Int: p(2), Rst: p(3)},
			SD:       &SD{Kind: "spi", SPIHost: 2, CS: p(21)}}
	}
	cases := []struct {
		name  string
		edit  func(*Board)
		error string
		warn  string
	}{
		{"fine", func(b *Board) {}, "", ""},
		{"octal PSRAM pin", func(b *Board) { b.LED = &LED{Kind: "gpio", Pin: p(35)} }, "octal PSRAM", ""},
		{"flash pin", func(b *Board) { b.LED = &LED{Kind: "gpio", Pin: p(28)} }, "flash and PSRAM", ""},
		{"no such pin", func(b *Board) { b.LED = &LED{Kind: "gpio", Pin: p(23)} }, "doesn't exist", ""},
		{"pin twice", func(b *Board) { b.LED = &LED{Kind: "gpio", Pin: p(21)} }, "GPIO 21 used twice", ""},
		{"strapping", func(b *Board) { b.LED = &LED{Kind: "gpio", Pin: p(0)} }, "", "strapping"},
		{"usb", func(b *Board) { b.LED = &LED{Kind: "gpio", Pin: p(19)} }, "", "USB port"},
		{"rgb", func(b *Board) { b.LED = &LED{Kind: "rgb", Red: p(4), Green: p(5), Blue: p(6), CommonAnode: true} }, "", ""},
		{"rgb pin twice", func(b *Board) { b.LED = &LED{Kind: "rgb", Red: p(4), Green: p(9), Blue: p(6)} }, "GPIO 9 used twice: SPI2 MOSI and LED green", ""},
		{"rgb without blue", func(b *Board) { b.LED = &LED{Kind: "rgb", Red: p(4), Green: p(5)} }, "led.blue: required", ""},
		{"undefined bus", func(b *Board) { b.SD.SPIHost = 3 }, "no such SPI bus", ""},
		{"no int", func(b *Board) { b.Ethernet.Int = nil }, "", "polled"},
		{"tx power", func(b *Board) { b.WiFi = &WiFi{TxPowerDBm: 25} }, "tx_power_dbm", ""},
		{"no emac on S3", func(b *Board) { b.Ethernet = &Ethernet{Kind: "emac"} }, "no built-in Ethernet MAC", ""},
		{"unknown image", func(b *Board) { b.Image = "esp8266" }, "not one of the chip images", ""},
		{"flash size", func(b *Board) { b.FlashMB = 12 }, "4, 8, 16 or 32", ""},
		{"address", func(b *Board) {
			b.Network = &nodecfg.Network{Address: "192.0.2.252/23", Gateway: "192.0.2.1"}
		}, "", ""},
		{"dhcp", func(b *Board) { b.Network = &nodecfg.Network{Address: "dhcp"} }, "", ""},
		{"address without gateway", func(b *Board) { b.Network = &nodecfg.Network{Address: "192.0.2.252/23"} }, "gateway: required", ""},
		{"address without prefix", func(b *Board) {
			b.Network = &nodecfg.Network{Address: "192.0.2.252", Gateway: "192.0.2.1"}
		}, "prefix length", ""},
		{"memory out of range", func(b *Board) { b.Memory = &Memory{CacheEntries: p(4)} }, "cache_entries", ""},
		{"psram out of range", func(b *Board) { b.PSRAMMB = p(100) }, "psram_mb: 8-64", ""},
		{"no psram", func(b *Board) { b.PSRAMMB = p(0) }, "psram_mb: espDNS needs at least 8 MB of PSRAM", ""},
		{"too little psram", func(b *Board) { b.PSRAMMB = p(4) }, "at least 8 MB of PSRAM", ""},
		{"psram left out", func(b *Board) { b.PSRAMMB = nil }, "", ""},
		{"index share of 0", func(b *Board) { b.PSRAMMB = p(8); b.Memory = &Memory{BlocklistIndexKB: p(0)} }, "", ""},
		{"no query log", func(b *Board) { b.PSRAMMB = p(8); b.Memory = &Memory{QueryLogKB: p(0)} }, "", ""},
		{"query log out of range", func(b *Board) { b.Memory = &Memory{QueryLogKB: p(70000)} }, "querylog_kb", ""},
		{"one upstream query", func(b *Board) { b.Memory = &Memory{FwdPending: p(1)} }, "fwd_pending", ""},
		{"upstream queries", func(b *Board) { b.PSRAMMB = p(8); b.Memory = &Memory{FwdPending: p(16)} }, "", ""},
		{"a query log that doesn't fit", func(b *Board) { b.PSRAMMB = p(8); b.Memory = &Memory{QueryLogKB: p(4096)} },
			"", "every service at once doesn't fit (PSRAM"},
		{"every service doesn't fit", func(b *Board) { b.PSRAMMB = p(8); b.Memory = &Memory{BlocklistKB: p(8000)} },
			"", "every service at once doesn't fit (PSRAM"},
		{"P4 needs fixed Ethernet", func(b *Board) {
			b.Image = "esp32p4"
			b.Ethernet.Optional = true
			b.SPI[0] = SPIBus{Host: 2, SCLK: p(7), MOSI: p(9), MISO: p(8)}
		}, "no Wi-Fi", ""},
		{"companion on a wired image", func(b *Board) {
			b.Image = "esp32p4-rev1"
			b.Ethernet = &Ethernet{Kind: "emac", Reset: p(51), MDC: p(31), MDIO: p(52)}
			b.WiFi = companion()
		}, "use esp32p4-c6", ""},
		{"hosted image without a companion", func(b *Board) {
			b.Image = "esp32p4-c6"
			b.Ethernet = &Ethernet{Kind: "emac", Reset: p(51), MDC: p(31), MDIO: p(52)}
			b.WiFi = nil
		}, "wifi companion", ""},
		{"companion on the SD card's slot", func(b *Board) {
			b.Image = "esp32p4-c6"
			b.Ethernet = &Ethernet{Kind: "emac", Reset: p(51), MDC: p(31), MDIO: p(52)}
			b.SD = &SD{Kind: "sdmmc", Slot: p(0), Width: 4}
			b.WiFi = companion()
			*b.WiFi.Companion.Slot = 0
		}, "wifi companion slot 0: the SD card is on SDMMC slot 0", ""},
		{"companion pins clear of the board's", func(b *Board) {
			b.Image = "esp32p4-c6"
			b.Ethernet = &Ethernet{Kind: "emac", Reset: p(51), MDC: p(31), MDIO: p(52)}
			b.SD = &SD{Kind: "sdmmc", Slot: p(0), Width: 4}
			b.WiFi = companion()
		}, "", ""},
		{"companion reset on the PHY reset", func(b *Board) {
			b.Image = "esp32p4-c6"
			b.Ethernet = &Ethernet{Kind: "emac", Reset: p(51), MDC: p(31), MDIO: p(52)}
			b.WiFi = companion()
			*b.WiFi.Companion.Reset = 51
		}, "GPIO 51 used twice: Ethernet PHY reset and Wi-Fi companion reset", ""},
		{"companion without a reset pin", func(b *Board) {
			b.Image = "esp32p4-c6"
			b.Ethernet = &Ethernet{Kind: "emac", Reset: p(51), MDC: p(31), MDIO: p(52)}
			b.WiFi = companion()
			b.WiFi.Companion.Reset = nil
		}, "wifi companion reset: required", ""},
		{"companion slot 2", func(b *Board) {
			b.Image = "esp32p4-c6"
			b.Ethernet = &Ethernet{Kind: "emac", Reset: p(51), MDC: p(31), MDIO: p(52)}
			b.WiFi = companion()
			*b.WiFi.Companion.Slot = 2
		}, "wifi companion slot", ""},
		{"companion chip other than a C6", func(b *Board) {
			b.Image = "esp32p4-c6"
			b.Ethernet = &Ethernet{Kind: "emac", Reset: p(51), MDC: p(31), MDIO: p(52)}
			b.WiFi = companion()
			b.WiFi.Companion.Chip = "esp32c5"
		}, "wifi companion chip", ""},
		{"companion on spi", func(b *Board) {
			b.Image = "esp32p4-c6"
			b.Ethernet = &Ethernet{Kind: "emac", Reset: p(51), MDC: p(31), MDIO: p(52)}
			b.WiFi = companion()
			b.WiFi.Companion.Bus = "spi"
		}, "wifi companion bus", ""},
		{"a hosted board's tx power cap", func(b *Board) {
			b.Image = "esp32p4-c6"
			b.Ethernet = &Ethernet{Kind: "emac", Reset: p(51), MDC: p(31), MDIO: p(52)}
			b.WiFi = companion()
			b.WiFi.TxPowerDBm = 18
		}, "", ""},
		{"tx power on a wired P4", func(b *Board) {
			b.Image = "esp32p4-rev1"
			b.Ethernet = &Ethernet{Kind: "emac", Reset: p(51), MDC: p(31), MDIO: p(52)}
			b.WiFi = &WiFi{TxPowerDBm: 18}
		}, "no Wi-Fi", ""},
		{"a P4 without Ethernet but with a companion", func(b *Board) {
			b.Image = "esp32p4-c6"
			b.Ethernet = nil
			b.WiFi = companion()
		}, "", ""}, // Wi-Fi is its network: no "needs Ethernet" error
	}
	for _, c := range cases {
		b := s3()
		c.edit(&b)
		errs, warns := Check(b)
		if c.error == "" && len(errs) > 0 {
			t.Errorf("%s: unexpected errors %v", c.name, errs)
		}
		if c.error != "" && !has(errs, c.error) {
			t.Errorf("%s: want an error with %q, got %v", c.name, c.error, errs)
		}
		if c.warn != "" && !has(warns, c.warn) {
			t.Errorf("%s: want a warning with %q, got %v", c.name, c.warn, warns)
		}
	}
}

// A node config's LED in place of the board's: only its own faults, its pins against the
// chip's and the board's others (the board's own LED pin free for it).
func TestCheckLED(t *testing.T) {
	b := Board{Name: "x", Image: "esp32s3-octal", SPI: []SPIBus{{Host: 2, SCLK: p(7), MOSI: p(9), MISO: p(8)}},
		Ethernet: &Ethernet{Kind: "w5500", SPIHost: 2, CS: p(1), Int: p(2), Rst: p(3)}, LED: &LED{Kind: "ws2812", Pin: p(38)}}
	for _, c := range []struct {
		led  LED
		want string
	}{
		{LED{Kind: "rgb", Red: p(38), Green: p(39), Blue: p(40), CommonAnode: true}, ""},
		{LED{Kind: "none"}, ""},
		{LED{Kind: "gpio", Pin: p(1)}, "GPIO 1 used twice: Ethernet CS and LED"},
		{LED{Kind: "rgb", Red: p(4), Green: p(35), Blue: p(6)}, "LED green: GPIO 35 is connected to the octal PSRAM"},
		{LED{Kind: "rgb", Red: p(4), Green: p(23), Blue: p(6)}, "LED green: GPIO 23 doesn't exist"},
		{LED{Kind: "ws2812"}, "led.pin: required"},
		{LED{Kind: "gpio", Pin: p(0)}, "LED: GPIO 0 is a strapping pin: boot mode depends on its level at reset"},
		{LED{Kind: "rgb", Red: p(4), Green: p(5), Blue: p(46)}, "LED blue: GPIO 46 is a strapping pin"},
		{LED{Kind: "ws2812", Pin: p(19)}, "LED: GPIO 19 is the USB port (logs and Improv)"},
		{LED{Kind: "gpio", Pin: p(43)}, "LED: GPIO 43 is the serial console (UART0)"},
	} {
		err := CheckLED(b, c.led)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("%+v: %v, want %q", c.led, err, c.want)
		}
	}

	// On a P4: strapping and USB pins refused; a caution pin the board's own LED is on is the
	// vendor's wiring, so it stays allowed.
	p4 := Board{Name: "y", Image: "esp32p4", Ethernet: &Ethernet{Kind: "emac"}, LED: &LED{Kind: "gpio", Pin: p(35)}}
	for _, c := range []struct {
		led  LED
		want string
	}{
		{LED{Kind: "gpio", Pin: p(35), ActiveLow: true}, ""},
		{LED{Kind: "ws2812", Pin: p(35)}, ""},
		{LED{Kind: "rgb", Red: p(35), Green: p(20), Blue: p(21)}, ""},
		{LED{Kind: "gpio", Pin: p(36)}, "LED: GPIO 36 is a strapping pin: boot mode depends on its level at reset"},
		{LED{Kind: "rgb", Red: p(20), Green: p(24), Blue: p(21)}, "LED green: GPIO 24 is the USB port (logs)"},
	} {
		err := CheckLED(p4, c.led)
		if c.want == "" && err != nil || c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)) {
			t.Errorf("p4 %+v: %v, want %q", c.led, err, c.want)
		}
	}
	// A board's own definition keeps them as warnings.
	if errs, warns := Check(p4); len(errs) != 0 || !slices.ContainsFunc(warns, func(w string) bool { return strings.Contains(w, "GPIO 35 is a strapping pin") }) {
		t.Errorf("p4 board: %v, %v", errs, warns)
	}
}

func TestParseRefusesUnknownFields(t *testing.T) {
	if _, err := Parse([]byte(`{"name":"x","image":"esp32p4","etherent":{"kind":"emac"}}`)); err == nil {
		t.Fatal("a misspelt field was accepted")
	}
}

// The node's address is per node: packed into its partition, never saved with a board.
func TestNetwork(t *testing.T) {
	b := Board{Name: "x", Image: "esp32s3-quad", Network: &nodecfg.Network{Address: "198.51.100.5/24", Gateway: "198.51.100.1"}}
	part, err := Entry{}.WithChanges(b).Pack()
	if err != nil || !strings.Contains(string(part[16:]), `"network":{"address":"198.51.100.5/24","gateway":"198.51.100.1"}`) {
		t.Fatal(err, string(part[16:80]))
	}
	if err := Save(t.TempDir(), b, nil); err == nil || !strings.Contains(err.Error(), "per node") {
		t.Fatal(err)
	}
	b.Network = nil
	if err := Save(t.TempDir(), b, nil); err != nil {
		t.Fatal(err)
	}
}

func TestPackFormat(t *testing.T) {
	def := []byte(`{"name":"x","image":"esp32p4"}`)
	part, err := PackJSON(def)
	if err != nil {
		t.Fatal(err)
	}
	if string(part[:4]) != "EDBD" || part[4] != 1 || int(part[8]) != len(def) || len(part) != 4096 || part[4095] != 0xFF {
		t.Fatalf("header % x", part[:16])
	}
}

func companion() *WiFi {
	return &WiFi{Companion: &Companion{Chip: "esp32c6", Bus: "sdio", Slot: p(1),
		CLK: p(18), CMD: p(19), D0: p(14), D1: p(15), D2: p(16), D3: p(17), Reset: p(54)}}
}

// HasWiFi is the image's, or the companion its board file describes: every S3 image has
// Wi-Fi, wired (esp32s3-eth) or not; a P4 has it only on esp32p4-c6 with a companion named
// in its file; and nor does a board whose image isn't a chip image.
func TestHasWiFi(t *testing.T) {
	for _, c := range []struct {
		image string
		wifi  *WiFi
		want  bool
	}{
		{"esp32s3-octal", nil, true}, {"esp32s3-eth", nil, true}, {"esp32s3-quad", nil, true},
		{"esp32p4", nil, false}, {"esp32p4-rev1", nil, false}, {"", nil, false},
		{"esp32p4-c6", companion(), true},
		{"esp32p4-c6", nil, false},                   // the image links esp_hosted; the board names no C6
		{"esp32p4-c6", &WiFi{TxPowerDBm: 18}, false}, // a tx cap is not a companion
		{"esp32p4-rev1", companion(), false},         // the image drives no radio
	} {
		if got := (Board{Image: c.image, WiFi: c.wifi}).HasWiFi(); got != c.want {
			t.Errorf("%s %+v: %v, want %v", c.image, c.wifi, got, c.want)
		}
	}
}

// MayRunOnWiFi: it has Wi-Fi, and no Ethernet, Ethernet of kind none, or Ethernet that may
// not be fitted. A wired board with Wi-Fi backup is not a Wi-Fi board (#78: it runs on its
// cable); a P4 board with no Wi-Fi at all is neither.
func TestMayRunOnWiFi(t *testing.T) {
	for _, c := range []struct {
		image string
		eth   *Ethernet
		wifi  *WiFi
		want  bool
	}{
		{"esp32s3-octal", nil, nil, true},
		{"esp32s3-octal", &Ethernet{Kind: "none"}, nil, true},
		{"esp32s3-octal", &Ethernet{Kind: "w5500", Optional: true}, nil, true},
		{"esp32s3-octal", &Ethernet{Kind: "w5500"}, nil, false},
		{"esp32s3-octal", &Ethernet{Kind: "emac"}, nil, false},
		{"esp32p4-c6", &Ethernet{Kind: "emac"}, companion(), false}, // Wi-Fi as the backup only
		{"esp32p4-c6", nil, companion(), true},
		{"esp32p4-rev1", nil, nil, false},
		{"esp32p4-rev1", nil, companion(), false},
	} {
		if got := (Board{Image: c.image, Ethernet: c.eth, WiFi: c.wifi}).MayRunOnWiFi(); got != c.want {
			t.Errorf("%s %+v %+v: %v, want %v", c.image, c.eth, c.wifi, got, c.want)
		}
	}
}
