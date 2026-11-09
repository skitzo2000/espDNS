/*
 * A wired board's Wi-Fi backup (net.c): when to switch between Ethernet and Wi-Fi. Wi-Fi runs
 * only while the cable is out. On every Ethernet link change, at boot and after each switch,
 * net.c stops the pending switch and, if netsw_next says one is due, starts it again with
 * netsw_hold_ms; when the hold ends it asks netsw_next once more and makes that switch. A
 * link that comes back (or goes again) within the hold so switches nothing. Pure, so the
 * host tests run it (tests/test_core.c).
 */
#pragma once

#include <stdbool.h>

/* Ethernet down this long before the node moves to Wi-Fi: the reconnect ladder's steady
 * state, the code's "a real outage" (net.c). */
#define NETSW_FAILOVER_MS 5000
/* Ethernet back up this long before the node moves back and stops Wi-Fi: the nag period, the
 * code's "the link has settled" (net.c). */
#define NETSW_FAILBACK_MS 10000

typedef enum { NETSW_STAY, NETSW_TO_WIFI, NETSW_TO_ETH } netsw_t;

/* The switch the links ask for: to Wi-Fi with Ethernet down, a network set and the node on
 * Ethernet; back to Ethernet with its link up and the node on the backup; else none. */
static inline netsw_t netsw_next(bool eth_link, bool on_backup, bool wifi_set)
{
    if (!eth_link && !on_backup && wifi_set)
        return NETSW_TO_WIFI;
    if (eth_link && on_backup)
        return NETSW_TO_ETH;
    return NETSW_STAY;
}

/* How long the link must hold before that switch is made. */
static inline int netsw_hold_ms(netsw_t s)
{
    return s == NETSW_TO_WIFI ? NETSW_FAILOVER_MS : s == NETSW_TO_ETH ? NETSW_FAILBACK_MS : 0;
}
