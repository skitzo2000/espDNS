# Hardware

An espDNS node is a small ESP32 board with Ethernet and a microSD card. To install one, see
[Getting started](getting-started.md#run-the-setup).

## Tested boards

These catalog boards ([`boards/`](../boards/)) are **tested**: they run on hardware for
every release.

| Board | Catalog name | Chip image | Flash | PSRAM | Network | microSD | LED |
|---|---|---|---|---|---|---|---|
| Guition JC-ESP32P4-M3-DEV | `p4-ip101` | `esp32p4-rev1` | 16 MB | 32 MB | Ethernet: built-in MAC + IP101 PHY | SDMMC, 4-bit | None usable: add one on a spare GPIO |
| Waveshare ESP32-S3-ETH | `ws-s3-eth` | `esp32s3-eth` | 16 MB | 8 MB | Ethernet: W5500 over SPI. Optional PoE module (802.3af) | SPI, its own bus | WS2812 RGB on GPIO 21 |
| Seeed XIAO ESP32S3 Sense | `xiao-s3-sense` | `esp32s3-octal` | 8 MB | 8 MB | Wi-Fi as it ships. Wired with an added W5500 module | SPI, on the Sense board | None usable: add one |

- **Guition ESP32-P4.** Top of the pecking order: 32 MB of PSRAM keeps large blocklists in
  RAM. Wired only; the on-board ESP32-C6 (Wi-Fi) isn't used. The entry is for P4 silicon
  v1.x. Silicon v3 or later needs the `esp32p4` chip image: describe it as your own board.
- **Waveshare ESP32-S3-ETH.** The RGB LED shows health in colour. With the PoE module one
  cable carries power and network, so a PoE switch on a UPS keeps both up. The W5500 runs
  at 20 MHz; the SD card's own SPI bus is slower than the P4's SDMMC.
- **Seeed XIAO ESP32S3 Sense.** Wi-Fi as it ships. For a wired node, add a W5500 on the
  Sense board's SPI bus, shared with the SD card: CS on D0 (GPIO 1), INT on D1 (GPIO 2), RST
  on D3 (GPIO 4). The user LED shares GPIO 21 with the SD card's chip select, so it's
  unusable. Wi-Fi transmit power is capped at 15 dBm (at 20 dBm it lost packets and the
  access point dropped it). PoE needs a separate splitter. The camera isn't used.

## What a board needs

| Part | Needed | Why |
|---|---|---|
| **Chip** | ESP32-P4 or ESP32-S3 today | The tested boards. Chip images exist for the classic ESP32, ESP32-C3 and ESP32-C6 too, but their memory sizes are first guesses and untested |
| **PSRAM** | Yes, in practice | Without PSRAM the DNS service's buffers don't fit in internal RAM. Boards without it wait for measured memory sizes |
| **Flash** | 4 MB or more | Two app slots, so an update can roll back. The image is about 1.1 MB |
| **Ethernet** | Strongly recommended | A built-in MAC with a PHY, or a W5500 over SPI. This is what reliable nodes use |
| **microSD** | Yes | Zone copies, hosted zones and blocklists live on the card |
| **LED** | Optional | Shows the node's health. A plain GPIO LED or a WS2812 |

### Chip images

Firmware is built once per chip family, not per board. A board picks its chip image; the
rest of the board is data the node reads at boot. Every image holds every driver its chip
can use (unused drivers cost flash, not RAM).

| Chip image | For |
|---|---|
| `esp32p4-rev1` | ESP32-P4 silicon v1.x |
| `esp32p4` | ESP32-P4 silicon v3 and later |
| `esp32s3-octal` | ESP32-S3 with octal PSRAM (S3R8, S3R16V) |
| `esp32s3-eth` | ESP32-S3 with octal PSRAM, wired (a W5500): `esp32s3-octal` made faster with internal RAM |
| `esp32s3-quad` | ESP32-S3 with quad PSRAM (S3R2) or none |
| `esp32` | Classic ESP32 |
| `esp32c3`, `esp32c6` | Single-core, no PSRAM |

`esp32s3-eth` doubles the instruction cache to 32 KB and puts the SPI master and lwIP's
packet path in IRAM. On the Waveshare board it answered 78.6% of a 3,000 queries/s flood
where `esp32s3-octal` answered 30.5%. The cost is about 29 KB less free internal RAM, which
a Wi-Fi S3 needs, so S3 boards on Wi-Fi stay on `esp32s3-octal`.

The two are **siblings** (same chip, PSRAM and flash layout): a node moves between them with
an ordinary firmware update, keeping its board definition, config, zones and lists. No USB
reflash.

### PSRAM

PSRAM holds the cache, the blocklist, the zones and the query log. Each board's definition
fixes each service's share, sized by testing, not guessed at run time:

| Board | Cache | Blocklist in RAM |
|---|---|---|
| ESP32-P4, 32 MB | 4 MB, 8192 answers | 20 MB: two 8 MB lists, so a new list swaps in live |
| ESP32-S3, 8 MB | 2.9 MB, 8192 answers | 2 MB: about 650,000 domains in RAM, about 1.3 million with the SD card |

The node and the controller both refuse a config that doesn't fit the board's plan. The full
memory table is in [`boards/README.md`](../boards/README.md).

### Ethernet

- **Built-in MAC + PHY** (`emac`), on the ESP32-P4 and the classic ESP32. PHYs: IP101,
  LAN87xx, RTL8201, DP83848.
- **W5500 over SPI** (`w5500`), on any chip. It can share an SPI bus with the SD card on its
  own chip-select pin. Mark it `optional` if the module may not be fitted. Wire INT if you
  can: without it the node polls every 10 ms, adding up to that much to each answer. The
  driver is a changed copy of ESP-IDF's (`firmware/components/w5500/README.md`).

### microSD

Over **SDMMC** (1- or 4-bit) or **SPI**. Zones and blocklists live on the card.

If the card fails or is missing at boot, the node keeps answering from its config and
forwarders, and copies secondary zones into RAM again. Hosted zones and blocking stay off
until the card is back, and the node reports **degraded: SD card**. That's a stopgap: let
the second node carry the load.

## Wired first, Wi-Fi as a fallback

At boot a node starts its board's Ethernet. If there's none, or it doesn't start (an
optional W5500 that isn't fitted), it uses Wi-Fi. It doesn't switch while running.

Wire the nodes you rely on. Wi-Fi is best effort:

- 2.4 GHz only.
- A new Wi-Fi node joins your network over USB at install (Install over USB, on Nodes).
- It reconnects on its own and reports **no network** while off the air.
- Power saving is off by default: it adds delay to every answer.
- Small boards often transmit more cleanly below the chip's 20 dBm. Set a cap in the
  board's definition, or per node in its config.
- A Wi-Fi node's config holds the Wi-Fi password, so the controller treats it as a secret.

The ESP32-P4 has no Wi-Fi of its own, and the P4 board above doesn't use its companion chip
yet, so it's wired only.

## Describing your own board

Any ESP32, S3, C3, C6 or P4 board can be described from its pinout. On the Nodes page, press
**+ Add node**, choose **A new board over USB**, and pick **Describe your own board…**. Or
pick a catalog board and press **Change it: describe a board from this one**. The form asks
for:

- name and title, chip image, flash size and PSRAM size;
- up to two SPI buses (host 2 or 3, and their SCLK, MOSI and MISO pins);
- Ethernet: none (Wi-Fi), built-in MAC + PHY (PHY type, address, reset, power, MDC, MDIO,
  RMII clock), or W5500 over SPI (bus, CS, INT, RST, clock);
- the SD card: none, SDMMC or SPI;
- the LED, and the Wi-Fi transmit power cap.

Pins are checked as you type: flash and PSRAM pins are refused, strapping pins get a
warning, and input-only and double-used pins are caught. The firmware checks again at boot.

Saved boards live in the `boards/` folder of the controller's data directory, and Install
over USB lists them alongside the catalog. They're JSON, in the same format as
[`boards/`](../boards/) ([field reference](../boards/README.md)):

```json
{
  "name": "my-s3-w5500",
  "title": "My ESP32-S3 board with a W5500",
  "image": "esp32s3-octal",
  "flash_mb": 16,
  "psram_mb": 8,
  "tier": "untested",
  "spi": [{ "host": 2, "sclk": 13, "mosi": 11, "miso": 12 }],
  "ethernet": { "kind": "w5500", "spi_host": 2, "cs": 14, "int": 10, "rst": 9 },
  "sd": { "kind": "spi", "spi_host": 2, "cs": 4 },
  "led": { "kind": "gpio", "pin": 48 }
}
```

Without a `memory` section a board gets its chip image's default sizes, which are first
guesses for anything but the P4 and the octal-PSRAM S3.

The address and keys aren't part of the board: Install over USB asks for the address on each
flash and writes your public keys into the node's keys partition alongside it.

Got a board that works? Let it join the flock as a **community** board: open a pull request
adding its file to `boards/`.

## Power and cost

**Power.** About 1 W per node; the P4 board measured 0.53 W, idle and at 200 queries a
second alike. Nodes never light-sleep (it would drop the link); idle nodes lower their CPU
clock instead, where the board allows. Power over USB, or the Waveshare board over PoE.

**Cost.** Cheap, cheep: one board, a microSD card and a cable or power supply per node. The
tested boards run roughly US$15 to US$40, plus a few dollars for a W5500 or PoE module
where you need one. Prices change, so check.
