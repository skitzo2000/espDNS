#include "keys_node.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "esp_flash.h"
#include "esp_log.h"
#include "esp_partition.h"
#include "keys.h"
#include "sdkconfig.h"

static const char *TAG = "keys";

/* The markers the controller reads in an image (keys_node.h): every image carries the
 * first; a build with a seed (KEYS_SEED=<dir>) also the second, with the seed's
 * fingerprints, worked out by CMakeLists.txt from the same files. */
static const char s_mark[] = KEYS_MARK;

#ifdef ESPDNS_KEYS_SEED
/* The keys built into this firmware (KEYS_SEED=<dir>: its release.pub and recovery.pub): the
 * seed for a partition that holds no keys record, and the fallback when the partition can't
 * be used. A key-neutral build has none of this. */
extern const uint8_t release_pub_start[] asm("_binary_release_pub_start");
extern const uint8_t release_pub_end[] asm("_binary_release_pub_end");
extern const uint8_t recovery_pub_start[] asm("_binary_recovery_pub_start");
extern const uint8_t recovery_pub_end[] asm("_binary_recovery_pub_end");
static const char s_seed_mark[] = KEYS_SEED_MARK ESPDNS_KEYS_SEED_FP;
#define SEED_RELEASE  release_pub_start
#define SEED_RECOVERY recovery_pub_start
#else
static const uint8_t s_no_seed[KEYS_PUB_LEN]; /* never used: seed_ok() says there is none */
#define SEED_RELEASE  s_no_seed
#define SEED_RECOVERY s_no_seed
#endif

/* The keys in use, and their fingerprints. Two copies: a KEYS release fills the one not in
 * use and switches s_cur to it (keys_node_apply), so a check running meanwhile reads either
 * the old keys or the new ones, never half of each. */
typedef struct {
    uint8_t key[2][KEYS_PUB_LEN];
    uint8_t retired[KEYS_PUB_LEN];
    bool has_retired;
    char fp[3][17];
} keyset_t;

static keyset_t s_set[2];
static keyset_t *volatile s_cur = &s_set[0];
static bool s_have, s_loaded;
static const char *s_source = "none";
static uint32_t s_gen;
static bool s_from_part;
static char s_err[256];
/* The keys partition and its slot in use (s_from_part), and what keys_node_write wrote. */
static const esp_partition_t *s_part;
static keys_slot_t s_slot, s_next;
static int s_which, s_next_which = -1;

const uint8_t *keys_node_key(int i) { return s_have && (i == 0 || i == 1) ? s_cur->key[i] : NULL; }
const uint8_t *keys_node_retired(void)
{
    keyset_t *k = s_cur;
    return s_have && k->has_retired ? k->retired : NULL;
}
const char *keys_node_fp(int i) { return s_have && i >= 0 && i < 3 ? s_cur->fp[i] : ""; }
uint64_t keys_node_seq(void) { return s_from_part ? s_slot.seq : 0; }
const char *keys_node_source(void) { return s_source; }
const char *keys_node_error(void) { return s_err; }
bool keys_node_setup(void) { return s_loaded && !s_have; }

bool keys_node_gen(uint32_t *gen)
{
    *gen = s_gen;
    return s_from_part;
}

/* Fills the copy not in use with these keys and switches to it. */
static void use_keys(const uint8_t *release, const uint8_t *recovery, const uint8_t *retired)
{
    keyset_t *k = s_cur == &s_set[0] ? &s_set[1] : &s_set[0];
    memcpy(k->key[0], release, KEYS_PUB_LEN);
    memcpy(k->key[1], recovery, KEYS_PUB_LEN);
    k->has_retired = retired != NULL;
    memset(k->retired, 0xFF, KEYS_PUB_LEN);
    k->fp[2][0] = 0;
    if (retired) {
        memcpy(k->retired, retired, KEYS_PUB_LEN);
        keys_fingerprint(retired, k->fp[2]);
    }
    keys_fingerprint(release, k->fp[0]);
    keys_fingerprint(recovery, k->fp[1]);
    __atomic_store_n(&s_cur, k, __ATOMIC_RELEASE);
}

static void use(const uint8_t *release, const uint8_t *recovery, const char *source)
{
    use_keys(release, recovery, NULL);
    s_have = true;
    s_source = source;
}

/* The slot in use from the keys partition: s, slot which of p. */
static void use_slot(const esp_partition_t *p, const keys_slot_t *s, int which, const char *source)
{
    use_keys(s->release, s->recovery, (s->flags & KEYS_FLAG_RETIRED) ? s->retired : NULL);
    s_have = true;
    s_source = source;
    s_part = p;
    s_slot = *s;
    s_which = which;
    s_gen = s->generation;
    s_from_part = true;
}

/* Whether the firmware has a seed that could be used: both points on P-256 and different. */
static bool seed_ok(void)
{
#ifdef ESPDNS_KEYS_SEED
    const char *volatile seed_mark = s_seed_mark; /* kept in the image */
    ESP_LOGI(TAG, "%s", seed_mark);
    if (release_pub_end - release_pub_start == KEYS_PUB_LEN && recovery_pub_end - recovery_pub_start == KEYS_PUB_LEN &&
        keys_point_ok(release_pub_start) && keys_point_ok(recovery_pub_start) &&
        memcmp(release_pub_start, recovery_pub_start, KEYS_PUB_LEN) != 0)
        return true;
    ESP_LOGE(TAG, "the keys built into this firmware are malformed: not used");
#endif
    return false;
}

static bool overlaps(uint32_t addr, uint32_t size)
{
    return addr < KEYS_PART_OFFSET + KEYS_PART_SIZE && KEYS_PART_OFFSET < addr + size;
}

/* The keys partition: the table's, or, on a table from before it, the same range registered
 * now if nothing in the table (or the table itself) is there. NULL with err on failure. */
static const esp_partition_t *keys_part(char *err, size_t cap)
{
    const esp_partition_t *p = esp_partition_find_first(ESP_PARTITION_TYPE_DATA,
                                                        (esp_partition_subtype_t)KEYS_PART_SUBTYPE, "keys");
    if (p) {
        if (p->address != KEYS_PART_OFFSET || p->size < KEYS_PART_SIZE) {
            snprintf(err, cap, "keys partition at 0x%lx (%lu bytes), not at 0x%x (%u bytes)",
                     (unsigned long)p->address, (unsigned long)p->size, KEYS_PART_OFFSET, KEYS_PART_SIZE);
            return NULL;
        }
        return p;
    }
    /* The bootloader and the table are below the table's end; the table lists the rest. */
    if (CONFIG_PARTITION_TABLE_OFFSET + 0x1000 > KEYS_PART_OFFSET) {
        snprintf(err, cap, "no keys partition, and the partition table (0x%x) is in its place",
                 CONFIG_PARTITION_TABLE_OFFSET);
        return NULL;
    }
    esp_partition_iterator_t it = esp_partition_find(ESP_PARTITION_TYPE_ANY, ESP_PARTITION_SUBTYPE_ANY, NULL);
    for (; it; it = esp_partition_next(it)) {
        const esp_partition_t *q = esp_partition_get(it);
        if (q->flash_chip == esp_flash_default_chip && overlaps(q->address, q->size)) {
            snprintf(err, cap, "no keys partition, and \"%s\" (0x%lx) is in its place", q->label,
                     (unsigned long)q->address);
            esp_partition_iterator_release(it);
            return NULL;
        }
    }
    esp_partition_iterator_release(it);
    esp_err_t e = esp_partition_register_external(esp_flash_default_chip, KEYS_PART_OFFSET, KEYS_PART_SIZE, "keys",
                                                  ESP_PARTITION_TYPE_DATA,
                                                  (esp_partition_subtype_t)KEYS_PART_SUBTYPE, &p);
    if (e != ESP_OK) {
        snprintf(err, cap, "no keys partition, and registering one failed: %s", esp_err_to_name(e));
        return NULL;
    }
    ESP_LOGW(TAG, "flash layout without a keys partition: using 0x%x-0x%x, which nothing in it uses",
             KEYS_PART_OFFSET, KEYS_PART_OFFSET + KEYS_PART_SIZE);
    return p;
}

/* Writes slot A from the seed (generation 1), slot B erased, and reads the whole partition
 * back into buf: it must pick slot A, with the seed's keys. erase_all: slot B isn't erased
 * yet (bytes left by something else). NULL if it took. */
static const char *seed(const esp_partition_t *p, bool erase_all, uint8_t *buf, keys_slot_t *out)
{
    keys_slot_t s = { .generation = 1, .source = KEYS_SRC_SEED };
    memcpy(s.release, SEED_RELEASE, KEYS_PUB_LEN);
    memcpy(s.recovery, SEED_RECOVERY, KEYS_PUB_LEN);
    memset(s.retired, 0xFF, KEYS_PUB_LEN);
    uint8_t rec[KEYS_REC_LEN];
    keys_pack(&s, rec);
    if (esp_partition_erase_range(p, 0, erase_all ? KEYS_PART_SIZE : KEYS_SLOT_SIZE) != ESP_OK)
        return "erase failed";
    if (esp_partition_write(p, 0, rec, sizeof(rec)) != ESP_OK)
        return "write failed";
    if (esp_partition_read(p, 0, buf, KEYS_PART_SIZE) != ESP_OK)
        return "read back failed";
    int which = -1;
    if (memcmp(rec, buf, sizeof(rec)) != 0 ||
        keys_pick(buf, buf + KEYS_SLOT_SIZE, KEYS_SLOT_SIZE, out, &which, NULL, 0) != KEYS_OK || which != 0)
        return "read back differs";
    return NULL;
}

#define SETUP_HINT ": setup mode (install the node over USB, or write its keys over USB)"

void keys_node_load(void)
{
    s_loaded = true;
    const char *volatile mark = s_mark; /* kept in the image */
    ESP_LOGI(TAG, "%s", mark);
#ifdef DNS2_TEST_NO_KEYS
    /* The test hook: the partition and any seed are ignored (nothing is read or written), so
     * the node comes up with no keys; a trial boot of it rolls back by itself (ota.c). */
    snprintf(s_err, sizeof(s_err), "test build (DNS2_TEST_NO_KEYS): the keys partition and any keys built in "
             "are ignored" SETUP_HINT);
    ESP_LOGE(TAG, "%s", s_err);
    return;
#endif
    bool have_seed = seed_ok();
    const char *also = have_seed ? "; using the keys built into this firmware" : "; no keys built in" SETUP_HINT;

    char err[120] = "";
    const esp_partition_t *p = keys_part(err, sizeof(err));
    uint8_t *buf = p ? malloc(KEYS_PART_SIZE) : NULL;
    if (p && !buf)
        snprintf(err, sizeof(err), "no memory to read the keys partition");
    if (buf && esp_partition_read(p, 0, buf, KEYS_PART_SIZE) != ESP_OK)
        snprintf(err, sizeof(err), "reading the keys partition failed");

    keys_slot_t s;
    int which = 0;
    char why[160] = "";
    keys_state_t st = err[0] ? KEYS_BLANK : keys_pick(buf, buf + KEYS_SLOT_SIZE, KEYS_SLOT_SIZE, &s, &which, why,
                                                    sizeof(why));
    keys_use_t u = keys_decide(!err[0], st, have_seed);
    s_source = keys_use_source(u);
    const char *serr;
    switch (u) {
    case KEYS_USE_SLOT:
        use_slot(p, &s, which, s_source);
        snprintf(s_err, sizeof(s_err), "%s", why); /* the other slot's trouble, if any */
        ESP_LOGI(TAG, "keys from slot %c, generation %lu (source %u)%s%s", 'A' + which, (unsigned long)s.generation,
                 s.source, why[0] ? ": " : "", why);
        if (have_seed && (memcmp(s.release, SEED_RELEASE, KEYS_PUB_LEN) != 0 ||
                          memcmp(s.recovery, SEED_RECOVERY, KEYS_PUB_LEN) != 0))
            ESP_LOGW(TAG, "the keys partition's keys differ from the ones built into this firmware: it wins");
        break;
    case KEYS_USE_SEED:
        /* No keys record was ever written here: erased, or bytes an earlier flash layout left
         * (neither slot has the magic), which are erased first. */
        if (st == KEYS_FOREIGN)
            ESP_LOGW(TAG, "keys partition held no keys record (%s): erasing it", why);
        if ((serr = seed(p, st == KEYS_FOREIGN, buf, &s)) != NULL) {
            snprintf(s_err, sizeof(s_err), "seeding the keys partition failed: %s%s", serr, also);
            use(SEED_RELEASE, SEED_RECOVERY, "embedded");
            ESP_LOGE(TAG, "%s", s_err);
            break;
        }
        use_slot(p, &s, 0, s_source);
        ESP_LOGW(TAG, "keys partition %s: seeded slot A from the keys built into this firmware",
                 st == KEYS_BLANK ? "was erased" : "erased");
        break;
    case KEYS_USE_EMBEDDED:
    case KEYS_USE_NONE:
        if (err[0])
            snprintf(s_err, sizeof(s_err), "%s%s", err, also);
        else /* a key-neutral build, and no keys record written yet */
            snprintf(s_err, sizeof(s_err), "the keys partition holds no keys record (%s) and this firmware has no "
                     "keys built in" SETUP_HINT, st == KEYS_BLANK ? "erased" : why);
        if (u == KEYS_USE_EMBEDDED)
            use(SEED_RELEASE, SEED_RECOVERY, s_source);
        ESP_LOGE(TAG, "%s", s_err);
        break;
    case KEYS_USE_CORRUPT_SEED:
    case KEYS_USE_CORRUPT_NONE:
        /* A slot with the magic: a keys record, damaged. Never seeded over: what is there may
         * be keys this firmware can't read. A firmware with keys built in trusts them
         * meanwhile; one without has none. */
        snprintf(s_err, sizeof(s_err), "no valid slot (%s)%s", why, also);
        if (u == KEYS_USE_CORRUPT_SEED)
            use(SEED_RELEASE, SEED_RECOVERY, s_source);
        ESP_LOGE(TAG, "keys partition: %s", s_err);
        break;
    }
    free(buf);
}

const char *keys_node_write(const keys_rel_t *r, uint64_t seq, char *err, size_t cap)
{
    s_next_which = -1;
    if (!s_from_part || !s_part) {
        snprintf(err, cap, "this node's keys are not from its keys partition (keys_source %s): write its keys over USB",
                 s_source);
        return err;
    }
    const char *why = keys_next(&s_slot, r, seq, &s_next);
    if (why) {
        snprintf(err, cap, "%s", why);
        return err;
    }
    /* The other slot: erased, written, then read back with the one in use. A power cut
     * before the record is whole leaves a slot that isn't valid, and the one in use, still
     * there, wins at boot. */
    int other = 1 - s_which;
    uint8_t rec[KEYS_REC_LEN];
    keys_pack(&s_next, rec);
    uint8_t *buf = malloc(KEYS_PART_SIZE);
    keys_slot_t got;
    int which = -1;
    if (!buf)
        why = "no memory";
    else if (esp_partition_erase_range(s_part, (size_t)other * KEYS_SLOT_SIZE, KEYS_SLOT_SIZE) != ESP_OK)
        why = "erasing the keys slot failed";
    else if (esp_partition_write(s_part, (size_t)other * KEYS_SLOT_SIZE, rec, sizeof(rec)) != ESP_OK)
        why = "writing the keys slot failed";
    else if (esp_partition_read(s_part, 0, buf, KEYS_PART_SIZE) != ESP_OK)
        why = "reading the keys slots back failed";
    else if (memcmp(buf + (size_t)other * KEYS_SLOT_SIZE, rec, sizeof(rec)) != 0 ||
             keys_pick(buf, buf + KEYS_SLOT_SIZE, KEYS_SLOT_SIZE, &got, &which, NULL, 0) != KEYS_OK ||
             which != other || got.generation != s_next.generation)
        why = "the keys slot read back differs";
    free(buf);
    if (why) {
        snprintf(err, cap, "%s: the keys in use are unchanged", why);
        ESP_LOGE(TAG, "KEYS release seq %llu: %s", (unsigned long long)seq, err);
        return err;
    }
    s_next_which = other;
    ESP_LOGW(TAG, "KEYS release seq %llu: slot %c written, generation %lu", (unsigned long long)seq, 'A' + other,
             (unsigned long)s_next.generation);
    return NULL;
}

void keys_node_apply(void)
{
    if (s_next_which < 0)
        return;
    keys_slot_t n = s_next;
    use_slot(s_part, &n, s_next_which, "partition");
    s_next_which = -1;
    s_err[0] = 0; /* the slot just written is valid, and so is the one before it */
    keyset_t *k = s_cur;
    ESP_LOGW(TAG, "keys now: release %s, recovery %s, retired %s (generation %lu)", k->fp[0], k->fp[1],
             k->fp[2][0] ? k->fp[2] : "none", (unsigned long)s_gen);
}
