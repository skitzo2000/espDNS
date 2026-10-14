/*
 * GET /zone?name=<zone>&from=<i>&limit=<n>&gen=<g> (ota.c serves it, private source
 * addresses only, as /querylog): a secondary zone's records as this node holds them in
 * memory, what it answers from, a page at a time, in the zone's canonical order:
 *
 *   {"name":"example.com","serial":2026101101,"gen":3,"records":1250,"from":0,
 *    "rrs":[{"o":"www.example.com","t":1,"ttl":3600,"d":"c0000201"},...],
 *    "next":200,"more":true}
 *
 * o: the owner (bytes other than printable ASCII, '"' and '\\' show as '?'); t: the type;
 * d: the rdata in hex, its names uncompressed. At most limit records (default ZR_LIMIT_DEF,
 * at most ZR_LIMIT_MAX); no record is started once the page holds ZR_BUDGET bytes, so one
 * large record makes a page of its own. gen changes whenever a refresh swaps the zone: given,
 * and not the zone's, the reply is 409 and the reader starts again from 0. A swap during a
 * page ends it after the last whole record (the next page gets the 409), or, inside a
 * record, closes the connection.
 *
 * Nothing is held between requests: a page is read one record (or one buffer's worth of a
 * large one) at a time under the registry's read lock, into the caller's buffer, and sent
 * with the lock released. Portable: run on the host in the tests.
 */
#pragma once

#include <stddef.h>
#include <stdint.h>

#define ZR_LIMIT_DEF 200
#define ZR_LIMIT_MAX 500
#define ZR_BUDGET    (32 * 1024)
#define ZR_HEAD      320  /* a record's start: its owner at its longest, type and TTL */
#define ZR_BUF       4096 /* the buffer a page is written through (more than ZR_HEAD + 96) */

/* Sends len bytes of the reply; 0: sent. */
typedef int (*zr_send_fn)(void *ctx, const char *buf, size_t len);

/* Page from..from+limit of the secondary zone name (gen UINT64_MAX: not given), through buf
 * (cap >= ZR_BUF) and send. Returns 200 (sent), 404 (no such zone, or no data for it yet)
 * or 409 (gen given and not the zone's): nothing sent then; or -1: a send failed, or the
 * zone was swapped inside a record (close the connection). */
int zread_page(const char *name, uint64_t from, uint64_t limit, uint64_t gen, char *buf, size_t cap,
               zr_send_fn send, void *ctx);
