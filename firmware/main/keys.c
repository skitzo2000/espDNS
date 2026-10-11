#include "keys.h"

#include <stdio.h>
#include <string.h>

#include "mbedtls/ecp.h"
#include "mbedtls/sha256.h"

static const uint8_t MAGIC[4] = { 'E', 'D', 'K', 'Y' };

#define OFF_VERSION  4
#define OFF_FLAGS    6
#define OFF_GEN      8
#define OFF_SOURCE   12
#define OFF_SEQ      16
#define OFF_RELEASE  24
#define OFF_RECOVERY 89
#define OFF_RETIRED  154
#define OFF_RSV      219
#define OFF_CRC      252

static uint32_t rd16(const uint8_t *p) { return (uint32_t)p[0] | (uint32_t)p[1] << 8; }
static uint32_t rd32(const uint8_t *p) { return rd16(p) | rd16(p + 2) << 16; }

static void wr16(uint8_t *p, uint32_t v)
{
    p[0] = (uint8_t)v;
    p[1] = (uint8_t)(v >> 8);
}

static void wr32(uint8_t *p, uint32_t v)
{
    wr16(p, v);
    wr16(p + 2, v >> 16);
}

uint32_t keys_crc32(const uint8_t *p, size_t n)
{
    uint32_t crc = 0xFFFFFFFFu;
    for (size_t i = 0; i < n; i++) {
        crc ^= p[i];
        for (int k = 0; k < 8; k++)
            crc = (crc >> 1) ^ (0xEDB88320u & (0u - (crc & 1u)));
    }
    return ~crc;
}

bool keys_point_ok(const uint8_t pub[KEYS_PUB_LEN])
{
    if (pub[0] != 0x04)
        return false;
    mbedtls_ecp_group grp;
    mbedtls_ecp_point q;
    mbedtls_ecp_group_init(&grp);
    mbedtls_ecp_point_init(&q);
    bool ok = mbedtls_ecp_group_load(&grp, MBEDTLS_ECP_DP_SECP256R1) == 0 &&
              mbedtls_ecp_point_read_binary(&grp, &q, pub, KEYS_PUB_LEN) == 0 &&
              mbedtls_ecp_check_pubkey(&grp, &q) == 0;
    mbedtls_ecp_point_free(&q);
    mbedtls_ecp_group_free(&grp);
    return ok;
}

bool keys_blank(const uint8_t *p, size_t n)
{
    for (size_t i = 0; i < n; i++)
        if (p[i] != 0xFF)
            return false;
    return true;
}

static bool zero(const uint8_t *p, size_t n)
{
    for (size_t i = 0; i < n; i++)
        if (p[i])
            return false;
    return true;
}

const char *keys_parse(const uint8_t rec[KEYS_REC_LEN], keys_slot_t *out)
{
    memset(out, 0, sizeof(*out));
    if (memcmp(rec, MAGIC, sizeof(MAGIC)) != 0)
        return "not a keys record";
    if (rd16(rec + OFF_VERSION) != KEYS_VERSION)
        return "unsupported version";
    if (keys_crc32(rec, OFF_CRC) != rd32(rec + OFF_CRC))
        return "bad CRC";
    out->flags = (uint16_t)rd16(rec + OFF_FLAGS);
    out->generation = rd32(rec + OFF_GEN);
    out->source = rec[OFF_SOURCE];
    out->seq = (uint64_t)rd32(rec + OFF_SEQ) | (uint64_t)rd32(rec + OFF_SEQ + 4) << 32;
    memcpy(out->release, rec + OFF_RELEASE, KEYS_PUB_LEN);
    memcpy(out->recovery, rec + OFF_RECOVERY, KEYS_PUB_LEN);
    memcpy(out->retired, rec + OFF_RETIRED, KEYS_PUB_LEN);
    if (out->flags & ~KEYS_FLAG_RETIRED)
        return "unknown flags";
    if (!zero(rec + OFF_SOURCE + 1, 3) || !zero(rec + OFF_RSV, OFF_CRC - OFF_RSV))
        return "reserved bytes set";
    if (out->generation == KEYS_GEN_INVALID)
        return "invalid generation";
    if (out->source < KEYS_SRC_USB || out->source > KEYS_SRC_RELEASE)
        return "unknown source";
    if (!keys_point_ok(out->release))
        return "release key is not a P-256 point";
    if (!keys_point_ok(out->recovery))
        return "recovery key is not a P-256 point";
    if (memcmp(out->release, out->recovery, KEYS_PUB_LEN) == 0)
        return "release and recovery keys are the same";
    if (out->flags & KEYS_FLAG_RETIRED) {
        if (!keys_point_ok(out->retired))
            return "retired key is not a P-256 point";
    } else if (!keys_blank(out->retired, KEYS_PUB_LEN)) {
        return "retired key set without its flag";
    }
    return NULL;
}

keys_state_t keys_pick(const uint8_t *a, const uint8_t *b, size_t len, keys_slot_t *out, int *which,
                       char *why, size_t whylen)
{
    const uint8_t *slot[2] = { a, b };
    keys_slot_t s[2];
    const char *err[2];
    bool blank[2];
    for (int i = 0; i < 2; i++) {
        blank[i] = keys_blank(slot[i], len);
        err[i] = len < KEYS_REC_LEN ? "short slot" : blank[i] ? "blank" : keys_parse(slot[i], &s[i]);
    }
    /* what is wrong with a slot that isn't erased */
    bool bad[2] = { err[0] && !blank[0], err[1] && !blank[1] };
    if (why && whylen) {
        why[0] = 0;
        if (bad[0] && bad[1])
            snprintf(why, whylen, "slot A: %s; slot B: %s", err[0], err[1]);
        else if (bad[0] || bad[1])
            snprintf(why, whylen, "slot %c: %s", bad[0] ? 'A' : 'B', err[bad[0] ? 0 : 1]);
    }
    if (blank[0] && blank[1])
        return KEYS_BLANK;
    int w = !err[0] && (err[1] || s[0].generation >= s[1].generation) ? 0 : !err[1] ? 1 : -1;
    if (w < 0) {
        bool magic = len >= sizeof(MAGIC) &&
                     (memcmp(a, MAGIC, sizeof(MAGIC)) == 0 || memcmp(b, MAGIC, sizeof(MAGIC)) == 0);
        return magic ? KEYS_CORRUPT : KEYS_FOREIGN;
    }
    *out = s[w];
    if (which)
        *which = w;
    return KEYS_OK;
}

void keys_pack(const keys_slot_t *s, uint8_t rec[KEYS_REC_LEN])
{
    memset(rec, 0, KEYS_REC_LEN);
    memcpy(rec, MAGIC, sizeof(MAGIC));
    wr16(rec + OFF_VERSION, KEYS_VERSION);
    wr16(rec + OFF_FLAGS, s->flags);
    wr32(rec + OFF_GEN, s->generation);
    rec[OFF_SOURCE] = s->source;
    wr32(rec + OFF_SEQ, (uint32_t)s->seq);
    wr32(rec + OFF_SEQ + 4, (uint32_t)(s->seq >> 32));
    memcpy(rec + OFF_RELEASE, s->release, KEYS_PUB_LEN);
    memcpy(rec + OFF_RECOVERY, s->recovery, KEYS_PUB_LEN);
    if (s->flags & KEYS_FLAG_RETIRED)
        memcpy(rec + OFF_RETIRED, s->retired, KEYS_PUB_LEN);
    else
        memset(rec + OFF_RETIRED, 0xFF, KEYS_PUB_LEN);
    wr32(rec + OFF_CRC, keys_crc32(rec, OFF_CRC));
}

void keys_fingerprint(const uint8_t pub[KEYS_PUB_LEN], char out[17])
{
    uint8_t h[32];
    mbedtls_sha256(pub, KEYS_PUB_LEN, h, 0);
    for (int j = 0; j < 8; j++)
        snprintf(out + 2 * j, 3, "%02x", h[j]);
}

static const uint8_t REL_MAGIC[4] = { 'E', 'D', 'K', 'R' };

#define REL_OFF_VERSION  4
#define REL_OFF_FLAGS    6
#define REL_OFF_RELEASE  8
#define REL_OFF_RECOVERY 73

const char *keys_rel_parse(const uint8_t *p, size_t n, keys_rel_t *out)
{
    memset(out, 0, sizeof(*out));
    if (n != KEYS_REL_LEN)
        return "a keys release's payload is 138 bytes";
    if (memcmp(p, REL_MAGIC, sizeof(REL_MAGIC)) != 0)
        return "not a keys release payload";
    if (rd16(p + REL_OFF_VERSION) != KEYS_REL_VERSION)
        return "unsupported keys release version";
    out->flags = (uint16_t)rd16(p + REL_OFF_FLAGS);
    if (out->flags & ~KEYS_REL_FLAG_RETIRE)
        return "unknown keys release flags";
    memcpy(out->release, p + REL_OFF_RELEASE, KEYS_PUB_LEN);
    memcpy(out->recovery, p + REL_OFF_RECOVERY, KEYS_PUB_LEN);
    if (!keys_point_ok(out->release))
        return "new release key is not a P-256 point";
    if (!keys_point_ok(out->recovery))
        return "new recovery key is not a P-256 point";
    if (memcmp(out->release, out->recovery, KEYS_PUB_LEN) == 0)
        return "new release and recovery keys are the same";
    return NULL;
}

void keys_rel_pack(const keys_rel_t *r, uint8_t out[KEYS_REL_LEN])
{
    memcpy(out, REL_MAGIC, sizeof(REL_MAGIC));
    wr16(out + REL_OFF_VERSION, KEYS_REL_VERSION);
    wr16(out + REL_OFF_FLAGS, r->flags);
    memcpy(out + REL_OFF_RELEASE, r->release, KEYS_PUB_LEN);
    memcpy(out + REL_OFF_RECOVERY, r->recovery, KEYS_PUB_LEN);
}

const char *keys_next(const keys_slot_t *cur, const keys_rel_t *r, uint64_t seq, keys_slot_t *out)
{
    memset(out, 0, sizeof(*out));
    memset(out->retired, 0xFF, KEYS_PUB_LEN);
    if (cur->generation + 1 >= KEYS_GEN_INVALID)
        return "keys generation exhausted: write the keys over USB";
    out->generation = cur->generation + 1;
    out->source = KEYS_SRC_RELEASE;
    out->seq = seq;
    memcpy(out->release, r->release, KEYS_PUB_LEN);
    memcpy(out->recovery, r->recovery, KEYS_PUB_LEN);
    if ((r->flags & KEYS_REL_FLAG_RETIRE) && memcmp(cur->release, r->release, KEYS_PUB_LEN) != 0) {
        out->flags = KEYS_FLAG_RETIRED;
        memcpy(out->retired, cur->release, KEYS_PUB_LEN);
    }
    return NULL;
}

keys_use_t keys_decide(bool part_ok, keys_state_t st, bool have_seed)
{
    if (!part_ok)
        return have_seed ? KEYS_USE_EMBEDDED : KEYS_USE_NONE;
    switch (st) {
    case KEYS_OK:
        return KEYS_USE_SLOT;
    case KEYS_BLANK:
    case KEYS_FOREIGN:
        return have_seed ? KEYS_USE_SEED : KEYS_USE_NONE;
    case KEYS_CORRUPT:
    default:
        /* Never seeded over: what is there may be keys this firmware can't read. */
        return have_seed ? KEYS_USE_CORRUPT_SEED : KEYS_USE_CORRUPT_NONE;
    }
}

const char *keys_use_source(keys_use_t u)
{
    switch (u) {
    case KEYS_USE_SLOT: return "partition";
    case KEYS_USE_SEED: return "seeded";
    case KEYS_USE_EMBEDDED: return "embedded";
    case KEYS_USE_CORRUPT_SEED:
    case KEYS_USE_CORRUPT_NONE: return "corrupt";
    case KEYS_USE_NONE:
    default: return "none";
    }
}

bool keys_use_setup(keys_use_t u) { return u == KEYS_USE_NONE || u == KEYS_USE_CORRUPT_NONE; }
