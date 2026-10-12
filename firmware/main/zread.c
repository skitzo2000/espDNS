#include "zread.h"

#include <stdbool.h>
#include <stdio.h>

#include "jsonw.h"
#include "registry.h"

/* Record i of z from *pos on (0: its start; then 1 + the rdata bytes written) at buf + *n,
 * as much as fits before cap. True once the record is whole (*pos is 0 again). */
static bool rr_json(const zone_t *z, uint64_t i, uint32_t *pos, char *buf, size_t cap, size_t *n)
{
    static const char hex[] = "0123456789abcdef";
    const zrr_t *r = &z->rrs[i];
    const uint8_t *d = zrr_rdata(z, r);
    if (*pos == 0) {
        char o[DNS_MAX_NAME];
        dns_name_to_str(zrr_owner(z, r), o, sizeof(o));
        *n += (size_t)snprintf(buf + *n, cap - *n, "{\"o\":\"%s\",\"t\":%u,\"ttl\":%lu,\"d\":\"", o, r->type,
                               (unsigned long)r->ttl);
        *pos = 1;
    }
    for (; *pos <= r->rdlen && *n + 2 <= cap; (*pos)++) {
        buf[(*n)++] = hex[d[*pos - 1] >> 4];
        buf[(*n)++] = hex[d[*pos - 1] & 15];
    }
    if (*pos <= r->rdlen || *n + 2 > cap)
        return false;
    buf[(*n)++] = '"';
    buf[(*n)++] = '}';
    *pos = 0;
    return true;
}

int zread_page(const char *name, uint64_t from, uint64_t limit, uint64_t gen, char *buf, size_t cap,
               zr_send_fn send, void *ctx)
{
    uint8_t apex[DNS_MAX_NAME];
    int alen = dns_name_from_str(name, apex);
    zslot_t *s = NULL;
    reg_rdlock();
    for (int k = 0; alen > 0 && k < reg_nslots(); k++)
        if (dns_name_eq(apex, alen, reg_slot(k)->apex, reg_slot(k)->apex_len))
            s = reg_slot(k);
    const zone_t *z = s ? s->z : NULL;
    uint32_t g = s ? s->gen : 0, serial = z ? z->serial : 0;
    uint64_t total = z ? z->n : 0;
    reg_unlock();
    if (!z)
        return 404;
    if (gen != UINT64_MAX && gen != g)
        return 409;

    /* z stays this zone while s->gen is g: a swap bumps it under the write lock. */
    char e[2 * DNS_MAX_NAME];
    size_t n = (size_t)snprintf(buf, cap, "{\"name\":%s,\"serial\":%lu,\"gen\":%lu,\"records\":%llu,\"from\":%llu,\"rrs\":[",
                                json_q(e, sizeof(e), s->name), (unsigned long)serial, (unsigned long)g,
                                (unsigned long long)total, (unsigned long long)from);
    size_t sent = 0;
    uint64_t i = from;
    uint32_t pos = 0;
    for (;;) {
        if (pos == 0 && (i >= total || i - from >= limit || sent + n >= ZR_BUDGET))
            break;
        if (cap - n < ZR_HEAD + 1) {
            if (send(ctx, buf, n))
                return -1;
            sent += n;
            n = 0;
        }
        reg_rdlock();
        bool same = s->gen == g;
        if (same && pos == 0 && i > from)
            buf[n++] = ',';
        bool done = same && rr_json(z, i, &pos, buf, cap, &n);
        reg_unlock();
        if (!same && pos)
            return -1;
        if (!same)
            break;
        i += done;
    }
    if (cap - n < 96) {
        if (send(ctx, buf, n))
            return -1;
        n = 0;
    }
    n += (size_t)snprintf(buf + n, cap - n, "],\"next\":%llu,\"more\":%s}\n", (unsigned long long)i,
                          i < total ? "true" : "false");
    return send(ctx, buf, n) ? -1 : 200;
}
