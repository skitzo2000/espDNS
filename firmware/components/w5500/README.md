# W5500 driver (our copy)

The WIZnet W5500's Ethernet driver, MAC RAW mode over SPI: the one a board with
`"ethernet": {"kind": "w5500"}` runs (`main/board.c`).

## Origin

Copied from ESP-IDF **v5.5.5** (`espressif/idf:v5.5.5@sha256:a9231d0697ab8f7517cc072e93b7c83e04907bfbfba80b6440d7dbbf90665cf2`,
the image `firmware/Makefile` pins), `components/esp_eth/src/spi/w5500/`:

| File | ESP-IDF file | Changes |
| --- | --- | --- |
| `esp_eth_mac_w5500.c` | `esp_eth_mac_w5500.c` | the include of `esp_eth_mac_spi.h` is `w5500_eth.h`; our changes (below) |
| `esp_eth_phy_w5500.c` | `esp_eth_phy_w5500.c` | the include of `esp_eth_phy.h` is `w5500_eth.h`; a line saying so under the SPDX header |
| `w5500.h` | `w5500.h` | |
| `w5500_ring.h` | | ours: espDNS's, under the repository's AGPL-3.0-only |
| `include/w5500_eth.h` | the W5500 parts of `include/esp_eth_mac_spi.h` and `include/esp_eth_phy.h` | |

Copyright Espressif Systems (Shanghai) CO LTD, under the Apache License 2.0 (`LICENSE`, ESP-IDF's);
each file keeps its SPDX header, and each one changed says so under it (Apache-2.0, section 4).
Apache-2.0 code may be combined into the AGPL-3.0 firmware; the repository's `README.md`,
License, says the same.

ESP-IDF's own W5500 driver stays out of the build: `CONFIG_ETH_SPI_ETHERNET_W5500` is off
(`firmware/sdkconfig.defaults`; `w5500_eth.h` refuses to build with it on), so the names here are
this component's alone. `CONFIG_ETH_USE_SPI_ETHERNET` stays on for the SPI driver types.

## Our changes

Fewer SPI transactions per frame; each change is marked `espdns:` in the code.

- **Receive**: a wake (the INT pin, or the poll timer on a board without one) clears RECV
  without reading the interrupt status first, then takes what is waiting: one burst read of
  RSR, RD and WR (checked against each other instead of reading RSR twice), then a chunk of
  up to 4 KB at a time, as many whole frames as it holds, RD past them and RECV, until all
  that the registers showed is taken. That is 5 transactions for a wake with up to 4 KB
  waiting. A frame landing meanwhile raises the interrupt again (it was cleared before the
  registers were read) and has the next wake, so a wake's work is bounded by the 16 KB
  buffer. Frames go to the stack only once RD is past them, so an SPI error never hands one
  on twice. A record with an impossible length drops all that was waiting from it (the
  stream is out of step) instead of stalling. ESP-IDF's driver reads each frame on its own:
  RSR twice, RD, the length, the frame, RD, RECV and its poll.
- **Send**: one read of Sn_TX_RD to see the frame before through (again until the link
  speed's timeout, as ESP-IDF's driver has it, if not yet), the frame (copied into a
  DMA-capable buffer first), Sn_TX_WR and SEND, and back to the stack without waiting for
  the frame to go: 4 transactions. Sn_TX_WR is ours to move, so it is kept instead of read
  per frame, and the buffer is known empty once the frame before is out, so Sn_TX_FSR is
  read only after a frame went unseen. SEND_OK is not used (it never raises the interrupt:
  Sn_IMR has RECV only; the receive task clears it with RECV). ESP-IDF's driver read
  Sn_TX_FSR twice and Sn_TX_WR, polled Sn_CR after SEND, then Sn_IR and PHYCFGR until
  SEND_OK, and cleared it: about 12 per frame.
- **Commands**: RECV and SEND are written without polling Sn_CR until the chip takes them; the
  next read of the RX registers shows a RECV taken (RSR caught up with RD) or reads them
  again and sends RECV again, and the next frame's Sn_TX_RD read covers a SEND. Sn_CR holds
  one command, so a command that follows the other task's within 100 us first reads Sn_CR
  until that one is taken (a SEND must not replace a RECV not yet taken, or the other way
  round). OPEN and CLOSE still wait, with the receive task and transmit kept out meanwhile,
  as they set the pointers afresh.
- **Not fitted**: the receive task uses the SPI only while the socket is open (start to
  stop), so a W5500 that doesn't answer (an optional add-on not fitted: init fails, the
  board layer deletes the MAC and uses Wi-Fi) is never touched by it.
- **Chunk size**: what one SPI transaction on the bus can move (`max_transfer_sz`, 4096 in
  `main/board.c`), up to 4 KB; the MAC is refused if the bus can't take one whole frame. A
  4 KB read holds the SPI lock about 1.6 ms at 20 MHz, so a frame to send waits that long at
  worst.

The ring pointer arithmetic, the walk over the RX buffer's frames and the chunked take are in
`w5500_ring.h`, tested on the host (`firmware/tests/test_w5500.c`) against a model of the
chip's buffer: pointers across the 16-bit wrap and the 16 KB end, chunks ending mid-record,
frames landing during a read, impossible lengths, and SPI errors at each step.
