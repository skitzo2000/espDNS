/*
 * espdns: the W5500 socket 0 buffer's pointer arithmetic, the walk over the frames waiting in
 * its RX buffer (MAC RAW) and the loop taking them a chunk at a time, apart from the SPI so
 * the host tests can run them (firmware/tests/test_w5500.c). Portable C, no ESP-IDF.
 *
 * The chip's buffer pointers (Sn_RX_RD, Sn_RX_WR, Sn_TX_WR, Sn_TX_RD) are 16-bit offsets that
 * run free and wrap at 64 KB; the chip maps them into the socket's buffer (16 KB here), a
 * burst over SPI included, so a read or write at a pointer never splits at the buffer's end.
 * The distance between two of them is their 16-bit difference.
 *
 * In MAC RAW mode each frame in the RX buffer is a record: a 2-byte big-endian length, the
 * length's own 2 bytes included, then the frame (no FCS).
 */
#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#define W5500_SOCK_BUF_SIZE 0x4000u /* socket 0 has all 16 KB of RX and of TX (w5500_setup_default) */
#define W5500_REC_HDR       2u
#define W5500_FRAME_MIN     60u   /* ETH_MIN_PACKET_SIZE - ETH_CRC_LEN: the chip passes no runts */
#define W5500_FRAME_MAX     1522u /* ETH_MAX_PACKET_SIZE */

/* Bytes from rd up to wr. */
static inline uint16_t w5500_ring_used(uint16_t rd, uint16_t wr)
{
    return (uint16_t)(wr - rd);
}

static inline uint16_t w5500_be16(const uint8_t *b)
{
    return (uint16_t)(b[0] << 8 | b[1]);
}

/* Sn_RX_RSR, Sn_RX_RD and Sn_RX_WR, read in one burst from Sn_RX_RSR (0x26..0x2B). */
typedef struct {
    uint16_t rsr, rd, wr;
} w5500_rx_regs_t;

static inline w5500_rx_regs_t w5500_rx_regs_parse(const uint8_t *b)
{
    w5500_rx_regs_t r = { w5500_be16(b), w5500_be16(b + 2), w5500_be16(b + 4) };
    return r;
}

/* Whether a burst read the registers whole. The chip updates each 16-bit register a byte at a
 * time and the burst reads them one after the other, so a frame landing during it can leave
 * one torn or RSR from before it and WR from after; RSR is WR - RD whenever none was (and the
 * chip has taken the last RECV), so the two agreeing proves the read. */
static inline bool w5500_rx_regs_ok(const w5500_rx_regs_t *r)
{
    return r->rsr <= W5500_SOCK_BUF_SIZE && r->rsr == w5500_ring_used(r->rd, r->wr);
}

/* How many bytes to read for the next chunk of the `avail` waiting: at most `cap` (a multiple
 * of 4, at least one whole record), rounded up to 4 for the SPI's DMA. The bytes past `avail`
 * are whatever the buffer holds there: reading them changes nothing in the chip. */
static inline uint32_t w5500_rx_chunk(uint32_t avail, uint32_t cap)
{
    uint32_t n = avail < cap ? avail : cap;
    return (n + 3u) & ~3u;
}

typedef enum {
    W5500_WALK_OK,  /* every record seen was whole and sound */
    W5500_WALK_BAD, /* a length no frame has, or a record running past what is waiting */
} w5500_walk_t;

/* Walks the records in buf, the first n bytes of the `avail` waiting from Sn_RX_RD (n <= avail),
 * handing each frame to frame(ctx, frame, len) (frame may be NULL: only measure), and sets *used
 * to the bytes the whole records took: where Sn_RX_RD goes next. A record cut by the end of buf
 * when more is waiting past it (n < avail) waits for the next chunk. BAD: the walk can't go on,
 * the buffer is out of step (*used is what came before). */
static inline w5500_walk_t w5500_rx_walk(const uint8_t *buf, uint32_t n, uint32_t avail,
                                         void (*frame)(void *ctx, const uint8_t *f, uint32_t len), void *ctx,
                                         uint32_t *used)
{
    uint32_t pos = 0;
    *used = 0;
    while (pos < n) {
        if (n - pos < W5500_REC_HDR)
            return n < avail ? W5500_WALK_OK : W5500_WALK_BAD;
        uint32_t rec = w5500_be16(buf + pos);
        if (rec < W5500_REC_HDR + W5500_FRAME_MIN || rec > W5500_REC_HDR + W5500_FRAME_MAX)
            return W5500_WALK_BAD;
        if (rec > n - pos)
            return n < avail && rec <= avail - pos ? W5500_WALK_OK : W5500_WALK_BAD;
        if (frame)
            frame(ctx, buf + pos + W5500_REC_HDR, rec - W5500_REC_HDR);
        pos += rec;
        *used = pos;
    }
    return W5500_WALK_OK;
}

/* The SPI side of w5500_rx_take, so the host tests can stand a model of the chip in for it.
 * read: `len` bytes of the RX buffer from pointer `at`; 0 when read. release: Sn_RX_RD = rd,
 * then RECV; < 0: RD was not written (nothing taken), 0: both done, > 0: RD written but RECV
 * not (taken; the next look at the registers sees RSR behind and sends RECV again). frame: one
 * frame for the stack. */
typedef struct {
    int (*read)(void *ctx, uint16_t at, uint8_t *buf, uint32_t len);
    int (*release)(void *ctx, uint16_t rd);
    void (*frame)(void *ctx, const uint8_t *f, uint32_t len);
    void *ctx;
} w5500_rx_io_t;

typedef enum {
    W5500_TAKE_OK,  /* all `rsr` bytes taken */
    W5500_TAKE_BAD, /* taken, but from *bad_at on dropped: out of step with the records */
    W5500_TAKE_IO,  /* an SPI error: what was taken before it stays taken, the rest waits */
} w5500_take_t;

/* Takes the `rsr` bytes waiting from Sn_RX_RD `rd` (one read of the registers), a chunk of up
 * to `cap` bytes at a time (a multiple of 4, at least one whole record; buf holds cap): reads
 * it, finds the whole records in it, moves RD past them and sends RECV (freeing the chip's
 * buffer as early as can be), then hands their frames on. A frame goes to the stack only once
 * RD is past it, so an SPI error never hands one on twice. Only what the registers showed is
 * taken: a frame landing meanwhile raises the interrupt again (the caller cleared it before
 * reading the registers), so the work per wake is bounded by the buffer's 16 KB. */
static inline w5500_take_t w5500_rx_take(const w5500_rx_io_t *io, uint8_t *buf, uint32_t cap, uint16_t rd,
                                         uint32_t rsr, uint16_t *bad_at)
{
    uint32_t done = 0;
    while (done < rsr) {
        uint32_t avail = rsr - done;
        uint32_t n = avail < cap ? avail : cap;
        if (io->read(io->ctx, (uint16_t)(rd + done), buf, w5500_rx_chunk(avail, cap)) != 0)
            return W5500_TAKE_IO;
        uint32_t used = 0;
        bool bad = w5500_rx_walk(buf, n, avail, NULL, NULL, &used) != W5500_WALK_OK;
        uint32_t skip = bad ? avail : used; /* out of step: drop all that was waiting */
        if (skip == 0)
            return W5500_TAKE_IO; /* not reached with cap >= one whole record */
        int rel = io->release(io->ctx, (uint16_t)(rd + done + skip));
        if (rel < 0)
            return W5500_TAKE_IO;
        uint32_t again = 0;
        w5500_rx_walk(buf, used, used, io->frame, io->ctx, &again);
        if (bad) {
            *bad_at = (uint16_t)(rd + done + used);
            return W5500_TAKE_BAD;
        }
        if (rel > 0)
            return W5500_TAKE_IO;
        done += used;
    }
    return W5500_TAKE_OK;
}
