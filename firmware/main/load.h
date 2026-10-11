/*
 * The DNS service under load: how many UDP queries the node received and how many
 * it dropped for want of a worker, for health ("overloaded") and /metrics.
 *
 * Received queries wait for a UDP worker in a queue of MP_UDP_QUEUE places (memplan.h). With
 * no place for a query, the receive task drops it, the newest, at once: a burst that fills
 * the queue for a moment costs a few queries, and a query in the queue waits at most a
 * queue's worth of answers. The receive task tells load_rx of each query it received, and
 * whether it was dropped; load_idle of each receive timeout.
 *
 * Overloaded (load_overloaded): over the last LOAD_WINDOW_MS, at least LOAD_SHED_PCT percent
 * of the queries received were dropped, and at least LOAD_SHED_MIN of them. A burst that
 * overfills the queue once drops a few of many; a flood the workers can't keep up with drops
 * a steady share. It clears once the window has passed with no more than that.
 *
 * A query that waited in the queue LOAD_DEADLINE_MS or more is shed, unanswered, by the worker
 * that takes it (load_stale, load_expired): its client has asked again or given up by then,
 * and answering it late only delays the queries behind it. Those count as dropped for
 * overloaded too, and a queue's worth of them (LOAD_EXPIRED_MIN) in the window is overloaded
 * on its own: the workers were held up for over a second, again and again.
 *
 * The queue's full time is kept for /metrics too: full from the first query it had no place
 * for until it has drained to LOAD_LOW_WATER.
 *
 * The counts are kept for the last LOAD_WINDOW_MS in LOAD_BUCKETS buckets, as health.h keeps
 * the forwarders' window: counts, never the time of the last drop, which a signed difference
 * would read as recent again once the ms clock is 2^31 past it (24.8 days); and buckets out of
 * the window start over when read, so none is read again when the ms clock comes round to it
 * (2^32 ms, 49.7 days). The receive task adds up its counts itself and hands them to the
 * window under the lock at most every LOAD_FLUSH_N queries or LOAD_FLUSH_MS, so a flood costs
 * it no lock per query.
 *
 * Portable C (pthreads): server.c uses it on the node, the tests on threads.
 */
#pragma once

#include <pthread.h>
#include <stdbool.h>
#include <stdint.h>

#include "memplan.h"

/* Overloaded (health_compute, HR_OVERLOADED), over the last LOAD_WINDOW_MS: at least
 * LOAD_SHED_PCT percent of the UDP queries received were dropped, and at least LOAD_SHED_MIN
 * (a queue's worth, MP_UDP_QUEUE) of them: a moment's burst isn't overloaded, a node that
 * fails one query in ten for want of a worker is. */
#define LOAD_WINDOW_MS 30000
/* A query waits for a worker at most this long; the one that takes it later sheds it. Stub
 * resolvers ask again after about a second (Windows), 5 s (glibc, dig). */
#define LOAD_DEADLINE_MS 1000
/* Overloaded, too: this many queries shed for their deadline in the window. */
#define LOAD_EXPIRED_MIN MP_UDP_QUEUE
#define LOAD_SHED_PCT  10
#define LOAD_SHED_MIN  MP_UDP_QUEUE
#define LOAD_BUCKET_MS 5000 /* the window kept in buckets of this */
#define LOAD_BUCKETS   (LOAD_WINDOW_MS / LOAD_BUCKET_MS)
/* Drained to this many queries waiting: the queue is no longer full (its full time). */
#define LOAD_LOW_WATER (MP_UDP_QUEUE / 2)
/* The receive task hands its counts to the window at least this often (queries, ms). */
#define LOAD_FLUSH_N  32
#define LOAD_FLUSH_MS 100

typedef struct {
    uint32_t epoch;    /* now_ms / LOAD_BUCKET_MS of what it counts */
    uint32_t received; /* UDP queries received */
    uint32_t dropped;  /* ...of them, dropped: the queue had no place */
    uint32_t expired;  /* ...of them, shed for their deadline */
} load_bucket_t;

typedef struct {
    pthread_mutex_t lock;
    load_bucket_t win[LOAD_BUCKETS];
    uint64_t full_ms_total; /* since boot, for /metrics */
    uint32_t expired_total; /* ...a 32-bit counter (it wraps) */
    /* The receive task's alone (load_rx, load_idle): what it hasn't handed on yet... */
    uint32_t p_epoch, p_received, p_dropped, p_full_ms, p_since_ms;
    /* ...and the queue full since full_ms (noted up to it). */
    bool full;
    uint32_t full_ms;
} load_t;

/* What health reads: the last LOAD_WINDOW_MS, give or take a bucket. */
typedef struct {
    uint32_t received;
    uint32_t dropped;
    uint32_t expired;
} load_window_t;

void load_init(load_t *l);
/* The receive task, on each query it received at now_ms: dropped, the queue had no place for
 * it; waiting, the queries in the queue now. */
void load_rx(load_t *l, bool dropped, int waiting, uint32_t now_ms);
/* The receive task, on each receive timeout: hands on what it has. */
void load_idle(load_t *l, int waiting, uint32_t now_ms);
/* A query that waited since rx_ms is past its deadline at now_ms. A signed difference: a
 * worker may read now_ms just before the receive task stamps a query. */
bool load_stale(uint32_t rx_ms, uint32_t now_ms);
/* A worker shed a query for its deadline at now_ms. Any worker may, at once. */
void load_expired(load_t *l, uint32_t now_ms);
/* The window at now_ms. It starts over the buckets out of it: health reads it every few
 * seconds, so none is still there when the ms clock comes round to it again (49.7 days). */
void load_get(load_t *l, uint32_t now_ms, load_window_t *w);
/* The totals since boot: the time the queue was full, the queries shed for their deadline. */
void load_totals(load_t *l, uint64_t *full_ms, uint32_t *expired);
/* Whether the window says overloaded (HR_OVERLOADED). */
bool load_overloaded(const load_window_t *w);
