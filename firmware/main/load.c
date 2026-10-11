#include "load.h"

void load_init(load_t *l)
{
    pthread_mutex_init(&l->lock, NULL);
    pthread_mutex_lock(&l->lock);
    /* No bucket's epoch is now's: each starts over when it is first used. */
    for (int i = 0; i < LOAD_BUCKETS; i++)
        l->win[i] = (load_bucket_t){ .epoch = UINT32_MAX };
    l->full_ms_total = 0;
    l->expired_total = 0;
    l->p_epoch = UINT32_MAX;
    l->p_received = l->p_dropped = l->p_full_ms = l->p_since_ms = 0;
    l->full = false;
    l->full_ms = 0;
    pthread_mutex_unlock(&l->lock);
}

static uint32_t sat_add(uint32_t a, uint32_t b) { return a + b < a ? UINT32_MAX : a + b; }

/* Epoch e's bucket, started over if it held an older one (under the lock). */
static load_bucket_t *bucket(load_t *l, uint32_t e)
{
    load_bucket_t *b = &l->win[e % LOAD_BUCKETS];
    if (b->epoch != e)
        *b = (load_bucket_t){ .epoch = e };
    return b;
}

/* The receive task's counts into the window, and its full time into the total. */
static void flush(load_t *l, uint32_t now_ms)
{
    l->p_since_ms = now_ms;
    if (!l->p_received && !l->p_full_ms)
        return;
    pthread_mutex_lock(&l->lock);
    /* Counted in a bucket the window has since left (nothing handed on for that long): it
     * would start over a newer one in its place. */
    if (l->p_received && now_ms / LOAD_BUCKET_MS - l->p_epoch < LOAD_BUCKETS) {
        load_bucket_t *b = bucket(l, l->p_epoch);
        b->received = sat_add(b->received, l->p_received);
        b->dropped = sat_add(b->dropped, l->p_dropped);
    }
    l->full_ms_total += l->p_full_ms;
    pthread_mutex_unlock(&l->lock);
    l->p_received = l->p_dropped = l->p_full_ms = 0;
}

static void note(load_t *l, uint32_t received, bool dropped, int waiting, uint32_t now_ms)
{
    uint32_t e = now_ms / LOAD_BUCKET_MS;
    /* A new bucket: what was counted goes in its own. */
    if (e != l->p_epoch) {
        flush(l, now_ms);
        l->p_epoch = e;
    }
    if (dropped && !l->full) {
        l->full = true;
        l->full_ms = now_ms;
    }
    if (l->full) {
        l->p_full_ms = sat_add(l->p_full_ms, now_ms - l->full_ms);
        l->full_ms = now_ms;
        if (!dropped && waiting <= LOAD_LOW_WATER)
            l->full = false; /* drained: the burst or the overload is over */
    }
    l->p_received += received;
    l->p_dropped += dropped;
    if (!received || l->p_received >= LOAD_FLUSH_N || now_ms - l->p_since_ms >= LOAD_FLUSH_MS)
        flush(l, now_ms);
}

void load_rx(load_t *l, bool dropped, int waiting, uint32_t now_ms) { note(l, 1, dropped, waiting, now_ms); }

void load_idle(load_t *l, int waiting, uint32_t now_ms) { note(l, 0, false, waiting, now_ms); }

bool load_stale(uint32_t rx_ms, uint32_t now_ms) { return (int32_t)(now_ms - rx_ms) >= LOAD_DEADLINE_MS; }

void load_expired(load_t *l, uint32_t now_ms)
{
    pthread_mutex_lock(&l->lock);
    load_bucket_t *b = bucket(l, now_ms / LOAD_BUCKET_MS);
    b->expired = sat_add(b->expired, 1);
    l->expired_total++;
    pthread_mutex_unlock(&l->lock);
}

void load_get(load_t *l, uint32_t now_ms, load_window_t *w)
{
    uint32_t e = now_ms / LOAD_BUCKET_MS;
    uint64_t received = 0, dropped = 0, expired = 0;
    pthread_mutex_lock(&l->lock);
    for (int i = 0; i < LOAD_BUCKETS; i++) {
        load_bucket_t *b = &l->win[i];
        /* This one or the ones before: the window; or the next, noted just after now_ms was
         * read (it counts as now). */
        uint32_t d = e - b->epoch;
        if (d < LOAD_BUCKETS || d == UINT32_MAX) {
            received += b->received;
            dropped += b->dropped;
            expired += b->expired;
        } else if (b->epoch != UINT32_MAX) {
            /* Out of the window: started over now, not when next used. A bucket left as it is
             * would be read as in the window again once the ms clock comes round to its epoch
             * (2^32 ms, 49.7 days on, with no query meanwhile to use it); health reads the
             * window far more often than that. */
            *b = (load_bucket_t){ .epoch = UINT32_MAX };
        }
    }
    pthread_mutex_unlock(&l->lock);
    w->received = received > UINT32_MAX ? UINT32_MAX : (uint32_t)received;
    w->dropped = dropped > UINT32_MAX ? UINT32_MAX : (uint32_t)dropped;
    w->expired = expired > UINT32_MAX ? UINT32_MAX : (uint32_t)expired;
}

void load_totals(load_t *l, uint64_t *full_ms, uint32_t *expired)
{
    pthread_mutex_lock(&l->lock);
    *full_ms = l->full_ms_total;
    *expired = l->expired_total;
    pthread_mutex_unlock(&l->lock);
}

bool load_overloaded(const load_window_t *w)
{
    /* The queries shed for their deadline were received, and not answered either. */
    uint64_t shed = (uint64_t)w->dropped + w->expired;
    return (shed >= LOAD_SHED_MIN && shed * 100 >= (uint64_t)w->received * LOAD_SHED_PCT) ||
           w->expired >= LOAD_EXPIRED_MIN;
}
