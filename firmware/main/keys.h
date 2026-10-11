/*
 * The keys partition (portable: needs only mbedTLS): the public keys a node trusts, kept in
 * flash apart from the firmware, so an image needs no keys built in (docs/architecture.md,
 * Signed releases and pins).
 *
 * The partition is two 4 KB sectors at 0x1b000, one slot each (A at +0, B at +0x1000). A
 * slot is a 256-byte record, little-endian, the rest of its sector 0xFF:
 *
 *   off  size  field
 *     0     4  magic "EDKY"
 *     4     2  version = 1
 *     6     2  flags: bit 0 = a retired release key is present
 *     8     4  generation: the valid slot with the higher one wins; 0xFFFFFFFF is invalid
 *    12     1  source: 1 USB install, 2 seeded from the keys built into the firmware,
 *                3 a KEYS release
 *    13     3  0
 *    16     8  seq of the KEYS release that wrote it (0 otherwise)
 *    24    65  release public key (0x04 || X || Y)
 *    89    65  recovery public key
 *   154    65  retired release public key (0xFF if none)
 *   219    33  0
 *   252     4  CRC-32 (IEEE, as zlib.crc32) of bytes 0..251
 *
 * A slot is valid when its magic, version, CRC and reserved bytes are right, both keys are
 * points on P-256 and differ, and the retired key is a point when the flag says so and
 * 0xFF otherwise. The record is not signed: only a USB install, or firmware that verified
 * a release signed with the recovery key, writes it. tools/keyspart.py writes the same
 * bytes (tests/keys_vectors.json checks both).
 */
#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#define KEYS_PART_OFFSET 0x1b000
#define KEYS_PART_SIZE   0x2000
#define KEYS_PART_SUBTYPE 0x42
#define KEYS_SLOT_SIZE   0x1000
#define KEYS_REC_LEN     256
#define KEYS_VERSION     1
#define KEYS_PUB_LEN     65
#define KEYS_GEN_INVALID 0xFFFFFFFFu
#define KEYS_FLAG_RETIRED 0x0001

enum { KEYS_SRC_USB = 1, KEYS_SRC_SEED = 2, KEYS_SRC_RELEASE = 3 };

typedef struct {
    uint16_t flags;
    uint32_t generation;
    uint8_t source;
    uint64_t seq;
    uint8_t release[KEYS_PUB_LEN];
    uint8_t recovery[KEYS_PUB_LEN];
    uint8_t retired[KEYS_PUB_LEN]; /* all 0xFF unless flags has KEYS_FLAG_RETIRED */
} keys_slot_t;

/* What keys_pick found. */
typedef enum {
    KEYS_OK,      /* a valid slot */
    KEYS_BLANK,   /* both slots erased (all 0xFF): nothing was ever written */
    KEYS_FOREIGN, /* no slot valid or erased, and neither has the magic: bytes left there by
                   * something else (an earlier flash layout), never a keys record */
    KEYS_CORRUPT, /* no valid slot, and one has the magic: a damaged keys record */
} keys_state_t;

/* CRC-32 (IEEE 802.3), as zlib.crc32. */
uint32_t keys_crc32(const uint8_t *p, size_t n);

/* Whether pub is an uncompressed point on P-256 (mbedtls_ecp_check_pubkey). */
bool keys_point_ok(const uint8_t pub[KEYS_PUB_LEN]);

/* Whether all n bytes are 0xFF (erased flash). */
bool keys_blank(const uint8_t *p, size_t n);

/* Checks a slot's record and fills *out. NULL if valid, else a short reason. */
const char *keys_parse(const uint8_t rec[KEYS_REC_LEN], keys_slot_t *out);

/* The slot to use from the two slots' contents (each len bytes: a whole sector, or at least
 * the record; blank means all len bytes are 0xFF). On KEYS_OK, *which is 0 (A) or 1 (B), the
 * valid one with the higher generation (A on a tie), and *out is it. With no valid slot it
 * tells a damaged keys record (a slot with the magic: KEYS_CORRUPT) from bytes that never
 * were one (KEYS_FOREIGN). why (may be NULL) gets what was wrong with the slots, "" if
 * nothing. */
keys_state_t keys_pick(const uint8_t *a, const uint8_t *b, size_t len, keys_slot_t *out, int *which,
                       char *why, size_t whylen);

/* The record for s, with its CRC. */
void keys_pack(const keys_slot_t *s, uint8_t rec[KEYS_REC_LEN]);

/* The first 8 bytes of SHA-256(pub), hex, as /status "keys" shows a key. */
void keys_fingerprint(const uint8_t pub[KEYS_PUB_LEN], char out[17]);

/*
 * A KEYS release (release.h REL_KEYS): the payload that rewrites a node's keys, 138 bytes,
 * little-endian:
 *
 *   off  size  field
 *     0     4  magic "EDKR"
 *     4     2  version = 1
 *     6     2  flags: bit 0 = keep the release key it replaces, as the retired key
 *     8    65  new release public key
 *    73    65  new recovery public key
 *
 * Signed with the node's current recovery key, for its node ID alone (rel_keys_check). The
 * node writes the slot it isn't using (keys_next: generation + 1, source 3, the release's
 * seq), reads it back, records the seq and uses the new keys at once; a power cut before
 * the write is complete leaves the old slot the valid one with the highest generation.
 *
 * The retired key (bit 0, a planned rotation) keeps the releases the node has stored, signed
 * with the old release key, loading at boot until the controller re-pushes them; never a
 * push. Without it (a compromised release key) they fail at the next boot until re-pushed.
 * A rotation that keeps the release key has no retired key.
 */
#define KEYS_REL_LEN          138
#define KEYS_REL_VERSION      1
#define KEYS_REL_FLAG_RETIRE  0x0001

typedef struct {
    uint16_t flags;
    uint8_t release[KEYS_PUB_LEN];
    uint8_t recovery[KEYS_PUB_LEN];
} keys_rel_t;

/* Checks a KEYS release's payload (n bytes) and fills *out. NULL if valid, else a reason. */
const char *keys_rel_parse(const uint8_t *p, size_t n, keys_rel_t *out);

/* The payload for r (tests; tools/release.py and the controller make the same bytes). */
void keys_rel_pack(const keys_rel_t *r, uint8_t out[KEYS_REL_LEN]);

/* The slot a KEYS release r with seq writes after cur (the slot in use): generation + 1,
 * source KEYS_SRC_RELEASE, r's keys, and as the retired key cur's release key when r keeps it
 * and changes it. NULL if it may be written, else a reason. */
const char *keys_next(const keys_slot_t *cur, const keys_rel_t *r, uint64_t seq, keys_slot_t *out);

/* What a node does at boot with what it found (keys_node.c), and so its keys_source:
 *   part_ok    the keys partition could be read (in the table, or registered at boot)
 *   st         keys_pick's verdict on it (only looked at with part_ok)
 *   have_seed  the firmware has keys built in (a KEYS_SEED= build) that could be used
 * A key-neutral build (no seed) falls back on nothing: with no valid slot it has no keys,
 * and is in setup mode (keys_use_setup). */
typedef enum {
    KEYS_USE_SLOT,         /* the valid slot: "partition" */
    KEYS_USE_SEED,         /* no keys record there: write slot A from the seed and use it,
                            * "seeded" (if the write fails: the seed, "embedded") */
    KEYS_USE_EMBEDDED,     /* the partition can't be used: the built-in keys, "embedded" */
    KEYS_USE_CORRUPT_SEED, /* a damaged keys record: the built-in keys meanwhile, "corrupt" */
    KEYS_USE_NONE,         /* nothing to use: setup mode, "none" */
    KEYS_USE_CORRUPT_NONE, /* a damaged keys record and no keys built in: setup mode, "corrupt" */
} keys_use_t;

keys_use_t keys_decide(bool part_ok, keys_state_t st, bool have_seed);
/* keys_source for u. */
const char *keys_use_source(keys_use_t u);
/* Whether u leaves the node without keys: setup mode (docs/operations.md, A node without keys). */
bool keys_use_setup(keys_use_t u);
