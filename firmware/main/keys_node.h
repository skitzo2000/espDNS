/*
 * The public keys this node trusts, read from the keys partition (keys.h) at boot.
 *
 * An image is key-neutral unless it was built with KEYS_SEED=<dir> (Makefile, Keys):
 * then it carries that directory's release.pub and recovery.pub as a seed. Where the keys
 * came from (/status "keys_source"; keys_decide in keys.h decides):
 *   partition  a valid slot in the keys partition
 *   seeded     the partition held no keys record (both slots erased, or bytes something
 *              else left there: neither slot has the magic; erased first) and the build has
 *              a seed. This boot wrote slot A (generation 1) from it and read it back;
 *              "partition" from the next boot
 *   corrupt    a slot has the magic but none is valid: a damaged keys record, never seeded
 *              over. A build with a seed trusts the seed meanwhile; a key-neutral build has
 *              no keys (setup mode). keys_error says which
 *   embedded   a build with a seed, and the partition can't be used (an old flash layout
 *              with something where it would be, a flash error, or the seed couldn't be
 *              written): the seed, as firmware from before the partition did
 *   none       no keys: a key-neutral build and no keys record (or a partition it can't
 *              use), or the DNS2_TEST_NO_KEYS test build
 *
 * No keys is setup mode (keys_node_setup): the node boots and answers DNS on its board
 * defaults (its stored config, zones and lists can't be verified), health says "no keys",
 * and /ota and /release refuse every release. Nothing over the network gives it keys: only
 * a USB install, or writing its keys over USB. A trial boot of firmware in setup mode rolls
 * back (ota.c, confirm_task).
 *
 * Once set up, a node's keys are rewritten over the network only by a KEYS release (keys.h)
 * signed with its recovery key: the slot not in use is written with the next generation and
 * the new keys are used at once, no reboot.
 *
 * A flash layout from before the keys partition (no "keys" entry in its table) gets it
 * registered at boot, at the same place, when nothing listed in the table is there; the
 * table itself is never rewritten.
 *
 * Every image carries the marker KEYS_MARK; one with a seed also KEYS_SEED_MARK followed by
 * the two keys' fingerprints, "<release>,<recovery>".
 */
#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "keys.h"

#define KEYS_MARK      "espdns:keys=partition-v1"
#define KEYS_SEED_MARK "espdns:keys-seed="

/* Reads (or seeds) the keys; once, at boot, before anything verifies a release. */
void keys_node_load(void);
/* Key i (0 release, 1 recovery) as a 65-byte point, or NULL for none. */
const uint8_t *keys_node_key(int i);
/* keys_source, as above. */
const char *keys_node_source(void);
/* The generation of the slot in use; false if the keys aren't from the partition. */
bool keys_node_gen(uint32_t *gen);
/* What is wrong, "" if nothing. */
const char *keys_node_error(void);
/* Whether the keys were loaded and there are none: setup mode. */
bool keys_node_setup(void);
/* The retired release key (keys.h, KEYS releases) as a 65-byte point, or NULL for none. */
const uint8_t *keys_node_retired(void);
/* Key i's fingerprint (0 release, 1 recovery, 2 retired), "" for none. */
const char *keys_node_fp(int i);
/* The seq of the KEYS release that wrote the slot in use (0 if none did). */
uint64_t keys_node_seq(void);

/*
 * A KEYS release (keys.h), in two steps, both under the update lock:
 *   keys_node_write  writes r into the slot not in use (keys_next) and reads it back; the
 *                    keys in use don't change. NULL if it took, else a reason (err)
 *   keys_node_apply  uses what keys_node_write wrote from now on: keys_node_key and the
 *                    others return the new keys (each pointer switches at once; the old
 *                    keys' bytes stay as they were until the next rotation)
 * Only keys from the keys partition can be rewritten so ("partition" or "seeded"): a node
 * without (setup mode, corrupt, embedded) gets its keys over USB.
 */
const char *keys_node_write(const keys_rel_t *r, uint64_t seq, char *err, size_t cap);
void keys_node_apply(void);
