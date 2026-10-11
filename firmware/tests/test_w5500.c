/*
 * The W5500 driver's buffer arithmetic, RX frame walk and chunked take (components/w5500/
 * w5500_ring.h), against a model of the chip's socket buffer: 16 KB, 16-bit pointers that run
 * free, reads and writes at a pointer wrapping inside the buffer as the chip's bursts do, RD
 * taken by RECV, and SPI errors where the driver can meet them.
 */
#include <assert.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "w5500_ring.h"

#define W5500_RX_CHUNK_TEST 8192 /* the largest chunk tried */

static int failures;
#define CHECK(c)                                                                                                       \
    do {                                                                                                               \
        if (!(c)) {                                                                                                    \
            fprintf(stderr, "%s:%d: CHECK(%s) failed\n", __FILE__, __LINE__, #c);                                    \
            failures++;                                                                                                \
        }                                                                                                              \
    } while (0)

/* ---- the chip's RX buffer, modelled ---- */

/* rd: the chip's own read pointer, moved by RECV; rd_reg: Sn_RX_RD as last written (RECV takes
 * it into rd); wr: Sn_RX_WR, moved by each frame that lands. RSR is wr - rd. */
typedef struct {
    uint8_t mem[W5500_SOCK_BUF_SIZE];
    uint16_t rd, rd_reg, wr;
} ring_t;

static uint16_t ring_free(const ring_t *r) { return W5500_SOCK_BUF_SIZE - w5500_ring_used(r->rd, r->wr); }

/* A frame arrives: its record goes in at WR, if it fits (the chip drops it otherwise). */
static int ring_put(ring_t *r, const uint8_t *frame, uint32_t len)
{
    uint32_t rec = len + W5500_REC_HDR;
    if (rec > ring_free(r))
        return 0;
    uint8_t hdr[2] = { (uint8_t)(rec >> 8), (uint8_t)rec };
    for (uint32_t i = 0; i < rec; i++)
        r->mem[(uint16_t)(r->wr + i) & (W5500_SOCK_BUF_SIZE - 1)] = i < 2 ? hdr[i] : frame[i - 2];
    r->wr += rec;
    return 1;
}

/* A burst read at a pointer: the address wraps inside the buffer. */
static void ring_read(const ring_t *r, uint16_t at, uint8_t *out, uint32_t n)
{
    for (uint32_t i = 0; i < n; i++)
        out[i] = r->mem[(uint16_t)(at + i) & (W5500_SOCK_BUF_SIZE - 1)];
}

/* The registers as a burst read sees them: RSR from the chip's rd, RD as written. */
static w5500_rx_regs_t ring_regs(const ring_t *r)
{
    w5500_rx_regs_t g = { w5500_ring_used(r->rd, r->wr), r->rd_reg, r->wr };
    return g;
}

/* ---- frames: each carries its sequence number and length, to check order and content ---- */

static void make_frame(uint8_t *f, uint32_t len, uint32_t seq)
{
    for (uint32_t i = 0; i < len; i++)
        f[i] = (uint8_t)(seq * 31 + i * 7);
    memcpy(f, &seq, sizeof(seq));
    memcpy(f + 4, &len, sizeof(len));
}

typedef struct {
    uint32_t next_seq, got, bad;
} sink_t;

static void sink(void *ctx, const uint8_t *f, uint32_t len)
{
    sink_t *s = ctx;
    uint32_t seq, flen;
    memcpy(&seq, f, sizeof(seq));
    memcpy(&flen, f + 4, sizeof(flen));
    uint8_t want[W5500_FRAME_MAX];
    make_frame(want, len, s->next_seq);
    if (seq != s->next_seq || flen != len || memcmp(want, f, len) != 0)
        s->bad++;
    s->next_seq++;
    s->got++;
}

/* ---- the driver's receive side against the model ---- */

/* What w5500_rx_take reaches through w5500_rx_io_t, on the model: a chunk read, RD and RECV
 * (each can be made to fail, as an SPI error would), the frames to the sink. Frames can land
 * during a chunk read, as on the wire. */
typedef struct {
    ring_t *r;
    sink_t *s;
    uint32_t cap;
    int reads, releases;           /* SPI calls this wake */
    int fail_read, fail_rd, fail_recv; /* fail one in this many (0: never) */
    unsigned rng;
    int land;                      /* frames to land during each chunk read */
    uint32_t *seq, *put;
} model_t;

static unsigned model_rand(model_t *m)
{
    m->rng = m->rng * 1103515245u + 12345u;
    return (m->rng >> 16) & 0x7fff;
}

static uint32_t frame_len(void)
{
    return (rand() % 4 == 0) ? W5500_FRAME_MIN + (uint32_t)(rand() % (W5500_FRAME_MAX - W5500_FRAME_MIN + 1))
                             : 60 + (uint32_t)(rand() % 120); /* mostly DNS-sized */
}

/* Frames land until one doesn't fit (dropped by the chip, as it would). */
static void land(ring_t *r, int k, uint32_t *seq, uint32_t *put)
{
    uint8_t f[W5500_FRAME_MAX];
    for (int b = 0; b < k; b++) {
        uint32_t len = frame_len();
        make_frame(f, len, *seq);
        if (!ring_put(r, f, len))
            return; /* full: the next frame takes this sequence number only if kept */
        (*seq)++;
        (*put)++;
    }
}

static int m_read(void *ctx, uint16_t at, uint8_t *buf, uint32_t len)
{
    model_t *m = ctx;
    m->reads++;
    CHECK(len % 4 == 0 && len <= m->cap);
    if (m->fail_read && model_rand(m) % m->fail_read == 0)
        return -1;
    ring_read(m->r, at, buf, len);
    if (m->land)
        land(m->r, m->land, m->seq, m->put);
    return 0;
}

static int m_release(void *ctx, uint16_t rd)
{
    model_t *m = ctx;
    m->releases++;
    /* never past what the chip has written, never backwards */
    CHECK(w5500_ring_used(m->r->rd, rd) <= w5500_ring_used(m->r->rd, m->r->wr));
    if (m->fail_rd && model_rand(m) % m->fail_rd == 0)
        return -1;
    m->r->rd_reg = rd;
    if (m->fail_recv && model_rand(m) % m->fail_recv == 0)
        return 1;
    m->r->rd = rd;
    return 0;
}

static void m_frame(void *ctx, const uint8_t *f, uint32_t len)
{
    model_t *m = ctx;
    sink(m->s, f, len);
}

/* One wake of the receive task (esp_eth_mac_w5500.c: emac_w5500_task, w5500_rx_drain): the
 * registers, read again (with RECV sent again) while RSR and RD disagree, as
 * w5500_rx_regs_read does; then w5500_rx_take. Returns what the take said, or -1 for nothing
 * waiting. */
static int wake(model_t *m)
{
    static uint8_t chunk[W5500_RX_CHUNK_TEST];
    w5500_rx_regs_t g = ring_regs(m->r);
    if (!w5500_rx_regs_ok(&g)) {
        m->r->rd = m->r->rd_reg; /* RECV again */
        g = ring_regs(m->r);
    }
    CHECK(w5500_rx_regs_ok(&g));
    if (g.rsr == 0)
        return -1;
    m->reads = m->releases = 0;
    uint16_t bad_at = 0;
    w5500_take_t t = w5500_rx_take(&(w5500_rx_io_t){ m_read, m_release, m_frame, m }, chunk, m->cap, g.rd, g.rsr, &bad_at);
    /* the work is bounded by what the registers showed: at least cap - one record per chunk */
    CHECK(m->reads <= 1 + (int)(g.rsr / (m->cap - (W5500_REC_HDR + W5500_FRAME_MAX) + 1)));
    return (int)t;
}

static void test_pointers(void)
{
    CHECK(w5500_ring_used(0, 0) == 0);
    CHECK(w5500_ring_used(0x1000, 0x1074) == 0x74);
    CHECK(w5500_ring_used(0xFFF0, 0x0010) == 0x20); /* across the 16-bit wrap */
    CHECK(w5500_ring_used(0xC000, 0x0000) == 0x4000);

    /* TX: Sn_TX_WR moves on by the frame's length, wrapping at 64 KB */
    uint16_t wr = 0xFFC0;
    wr += 100;
    CHECK(wr == 0x0024);
    CHECK(w5500_ring_used(0xFFC0, wr) == 100);

    CHECK(w5500_rx_chunk(0, 4096) == 0);
    CHECK(w5500_rx_chunk(1, 4096) == 4);
    CHECK(w5500_rx_chunk(116, 4096) == 116);
    CHECK(w5500_rx_chunk(117, 4096) == 120);
    CHECK(w5500_rx_chunk(4096, 4096) == 4096);
    CHECK(w5500_rx_chunk(16000, 4096) == 4096);
}

static void test_regs(void)
{
    const uint8_t whole[8] = { 0x00, 0x74, 0xFF, 0xF0, 0x00, 0x64, 0, 0 }; /* RSR 0x74, RD 0xFFF0, WR 0x0064 */
    w5500_rx_regs_t r = w5500_rx_regs_parse(whole);
    CHECK(r.rsr == 0x74 && r.rd == 0xFFF0 && r.wr == 0x0064);
    CHECK(w5500_rx_regs_ok(&r));

    /* a frame landed between RSR and WR: RSR from before, WR from after */
    const uint8_t later[8] = { 0x00, 0x74, 0xFF, 0xF0, 0x00, 0xD8, 0, 0 };
    r = w5500_rx_regs_parse(later);
    CHECK(!w5500_rx_regs_ok(&r));

    /* RSR torn: its high byte from before a frame, its low byte from after */
    const uint8_t torn[8] = { 0x00, 0x10, 0x10, 0x00, 0x11, 0x10, 0, 0 }; /* true RSR 0x0110 */
    r = w5500_rx_regs_parse(torn);
    CHECK(!w5500_rx_regs_ok(&r));

    /* nothing waiting */
    const uint8_t empty[8] = { 0, 0, 0x12, 0x34, 0x12, 0x34, 0, 0 };
    r = w5500_rx_regs_parse(empty);
    CHECK(w5500_rx_regs_ok(&r) && r.rsr == 0);

    /* more than the buffer holds: not a read of this chip */
    w5500_rx_regs_t big = { 0x8000, 0, 0x8000 };
    CHECK(!w5500_rx_regs_ok(&big));
    w5500_rx_regs_t full = { 0x4000, 0xF000, 0x3000 };
    CHECK(w5500_rx_regs_ok(&full));
}

static void walk_buf(uint8_t *buf, const uint32_t *lens, int k, uint32_t seq0)
{
    uint32_t pos = 0;
    for (int i = 0; i < k; i++) {
        uint32_t rec = lens[i] + 2;
        buf[pos] = (uint8_t)(rec >> 8);
        buf[pos + 1] = (uint8_t)rec;
        make_frame(buf + pos + 2, lens[i], seq0 + (uint32_t)i);
        pos += rec;
    }
}

static void test_walk(void)
{
    static uint8_t buf[8192];
    uint32_t used;
    sink_t s = { 0 };

    /* three frames, all there */
    const uint32_t lens[] = { 60, 115, 1514 };
    walk_buf(buf, lens, 3, 0);
    uint32_t total = 62 + 117 + 1516;
    CHECK(w5500_rx_walk(buf, total, total, sink, &s, &used) == W5500_WALK_OK);
    CHECK(used == total && s.got == 3 && s.bad == 0);

    /* the chunk ends inside the third, more waiting: the first two, the third next time */
    s = (sink_t){ 0 };
    CHECK(w5500_rx_walk(buf, 62 + 117 + 100, total, sink, &s, &used) == W5500_WALK_OK);
    CHECK(used == 62 + 117 && s.got == 2);

    /* the chunk ends inside the second's length field, more waiting */
    s = (sink_t){ 0 };
    CHECK(w5500_rx_walk(buf, 63, total, sink, &s, &used) == W5500_WALK_OK);
    CHECK(used == 62 && s.got == 1);

    /* a record running past all that is waiting: out of step */
    s = (sink_t){ 0 };
    CHECK(w5500_rx_walk(buf, 62 + 117 + 100, 62 + 117 + 100, sink, &s, &used) == W5500_WALK_BAD);
    CHECK(used == 62 + 117 && s.got == 2);
    s = (sink_t){ 0 };
    CHECK(w5500_rx_walk(buf, 63, 63, sink, &s, &used) == W5500_WALK_BAD);
    CHECK(used == 62);

    /* impossible lengths: 0, a runt, past the largest frame */
    const uint16_t badlen[] = { 0, 1, 2, 61, 2 + 1523, 0xFFFF };
    for (size_t i = 0; i < sizeof(badlen) / sizeof(badlen[0]); i++) {
        memset(buf, 0, 2048);
        buf[0] = (uint8_t)(badlen[i] >> 8);
        buf[1] = (uint8_t)badlen[i];
        s = (sink_t){ 0 };
        CHECK(w5500_rx_walk(buf, 2048, 2048, sink, &s, &used) == W5500_WALK_BAD);
        CHECK(used == 0 && s.got == 0);
    }
    /* the smallest and largest frames are fine */
    const uint32_t edge[] = { W5500_FRAME_MIN, W5500_FRAME_MAX };
    walk_buf(buf, edge, 2, 0);
    s = (sink_t){ 0 };
    total = 2 * 2 + W5500_FRAME_MIN + W5500_FRAME_MAX;
    CHECK(w5500_rx_walk(buf, total, total, sink, &s, &used) == W5500_WALK_OK);
    CHECK(used == total && s.got == 2 && s.bad == 0);

    /* nothing */
    s = (sink_t){ 0 };
    CHECK(w5500_rx_walk(buf, 0, 0, sink, &s, &used) == W5500_WALK_OK && used == 0 && s.got == 0);
}

/* Frames of every size through the modelled buffer, many times round, the pointers starting
 * near the 16-bit wrap: every frame comes out whole, once and in order. Bursts of arrivals
 * between wakes fill the buffer (the chip drops what doesn't fit, as it would), and frames
 * land during the chunk reads too: those wait for the next wake. */
static void test_ring(uint32_t cap, uint16_t start, int land_during)
{
    static ring_t r;
    memset(&r, 0, sizeof(r));
    r.rd = r.rd_reg = r.wr = start;
    sink_t s = { 0 };
    uint32_t seq = 0, put = 0;
    model_t m = { .r = &r, .s = &s, .cap = cap, .seq = &seq, .put = &put };
    srand(cap + start + (unsigned)land_during);
    for (int iter = 0; iter < 20000; iter++) {
        land(&r, 1 + rand() % 40, &seq, &put);
        m.land = land_during;
        CHECK(wake(&m) == W5500_TAKE_OK);
        m.land = 0;
        /* what landed meanwhile raised the interrupt again: the next wake takes it */
        int t = wake(&m);
        CHECK(t == -1 || t == W5500_TAKE_OK);
        CHECK(r.rd == r.wr && r.rd_reg == r.wr);
    }
    CHECK(s.got == put && s.bad == 0);
    CHECK(put > 100000);
}

/* SPI errors anywhere (a chunk read, RD, RECV): what was taken stays taken, the rest waits for
 * the next wake, and no frame reaches the stack twice or goes missing. */
static void test_ring_errors(uint32_t cap, uint16_t start)
{
    static ring_t r;
    memset(&r, 0, sizeof(r));
    r.rd = r.rd_reg = r.wr = start;
    sink_t s = { 0 };
    uint32_t seq = 0, put = 0;
    model_t m = { .r = &r, .s = &s, .cap = cap, .seq = &seq, .put = &put, .rng = start + cap,
                  .fail_read = 5, .fail_rd = 7, .fail_recv = 6 };
    srand(start * 3 + cap);
    int io = 0;
    for (int iter = 0; iter < 20000; iter++) {
        land(&r, 1 + rand() % 40, &seq, &put);
        for (int w = 0; w < 1000; w++) {
            int t = wake(&m);
            if (t == -1)
                break;
            CHECK(t == W5500_TAKE_OK || t == W5500_TAKE_IO);
            io += t == W5500_TAKE_IO;
        }
        CHECK(r.rd == r.wr);
    }
    CHECK(io > 1000);
    CHECK(s.got == put && s.bad == 0);
}

/* Out of step: a record with an impossible length drops all that was waiting from it, the
 * frames before it go up, and frames after the drop come out right again. */
static void test_ring_bad(uint32_t cap)
{
    static ring_t r;
    memset(&r, 0, sizeof(r));
    r.rd = r.rd_reg = r.wr = 0xFFF0;
    sink_t s = { 0 };
    uint32_t seq = 0, put = 0;
    model_t m = { .r = &r, .s = &s, .cap = cap, .seq = &seq, .put = &put };
    uint8_t f[W5500_FRAME_MAX];
    /* three frames, the second's length field broken: the first goes up, the rest is dropped */
    for (int i = 0; i < 3; i++) {
        make_frame(f, 100, seq++);
        CHECK(ring_put(&r, f, 100));
    }
    uint16_t second = (uint16_t)(r.rd + 102);
    r.mem[second & (W5500_SOCK_BUF_SIZE - 1)] = 0xFF;
    CHECK(wake(&m) == W5500_TAKE_BAD);
    CHECK(s.got == 1 && s.bad == 0);
    CHECK(r.rd == r.wr && r.rd_reg == r.wr);
    /* in step again */
    s.next_seq = seq;
    for (int i = 0; i < 50; i++) {
        make_frame(f, 60 + (uint32_t)i * 7, seq++);
        CHECK(ring_put(&r, f, 60 + (uint32_t)i * 7));
    }
    CHECK(wake(&m) == W5500_TAKE_OK);
    CHECK(s.got == 51 && s.bad == 0 && r.rd == r.wr);

    /* a record claiming more than was waiting, far into a long run: dropped from there */
    s = (sink_t){ 0 };
    s.next_seq = seq;
    uint32_t before = 0;
    for (int i = 0; i < 100; i++) {
        make_frame(f, 120, seq++);
        CHECK(ring_put(&r, f, 120));
    }
    uint16_t last = (uint16_t)(r.wr - 122);
    r.mem[last & (W5500_SOCK_BUF_SIZE - 1)] = 0x05; /* 0x05xx: 1280 and more, past what is waiting */
    before = 99;
    CHECK(wake(&m) == W5500_TAKE_BAD);
    CHECK(s.got == before && s.bad == 0 && r.rd == r.wr);
}

int main(void)
{
    test_pointers();
    test_regs();
    test_walk();
    test_ring(4096, 0, 0);
    test_ring(4096, 0xFF00, 5);
    test_ring(2048, 0xC123, 3);
    test_ring(W5500_RX_CHUNK_TEST, 0x3FFE, 10);
    test_ring(1524, 0x3FFE, 2); /* the smallest chunk: one largest record */
    test_ring_errors(4096, 0xFFA0);
    test_ring_errors(1524, 0x1234);
    test_ring_bad(4096);
    test_ring_bad(1524);
    if (failures) {
        fprintf(stderr, "test_w5500: %d failed\n", failures);
        return 1;
    }
    printf("test_w5500: ok\n");
    return 0;
}
