/*
 * Network service: one live interface, Ethernet if the board has it and it starts, otherwise
 * Wi-Fi (on chips that have it) with the network from the node config, else the one saved in
 * NVS by Improv. A wired board with a Wi-Fi network set moves to Wi-Fi while its cable is out
 * and back once it is in again (netsw.h), keeping its address: every function below follows
 * the live interface. The address is the static one the settings give (the node config's, the
 * board definition's or the build's); DHCP only if a setting asks for it, else none: the node
 * never sends DHCP on its own. The node advertises _espdns._tcp over
 * mDNS as espdns-<last 3 bytes of its MAC>.local so a controller can find it.
 */
#pragma once

#include <stdbool.h>
#include <stdint.h>

#include "sdkconfig.h"

/* The image has a Wi-Fi driver: the chip's own (the S3), or a companion chip's over
 * esp_hosted (a P4 with a C6: esp32p4-c6, which links esp_wifi_remote and answers the same
 * esp_wifi_* calls over SDIO). Everything Wi-Fi in net.c and improv.c is built only then, so
 * an image without one links none of it. SOC_WIFI_SUPPORTED is the wrong test: it is the
 * chip's own radio, false on a P4 whose Wi-Fi is the companion's. */
#if ESPDNS_WIFI
#define NET_HAS_WIFI 1
#else
#define NET_HAS_WIFI 0
#endif

typedef enum { NET_NONE, NET_ETHERNET, NET_WIFI } net_kind_t;

/* Wi-Fi link history since boot, so a badly placed or steered node shows up as such. */
typedef struct {
    uint32_t connects;        /* times associated with the access point */
    uint32_t drops;           /* a working link lost */
    uint32_t failed_attempts; /* connect attempts that didn't associate */
    uint32_t last_join_ms;    /* time from connect attempt to associated, last time */
    uint8_t bssid[6];         /* access point of the last association */
    uint8_t channel;
} net_wifi_stats_t;

/* Starts the network. Non-blocking: link and address come later, by events. */
void net_start(void);
/* The live interface: NET_WIFI on a wired board's Wi-Fi backup too. */
net_kind_t net_kind(void);
const char *net_kind_name(void);
/* A wired board answering on its Wi-Fi backup (its Ethernet link is down). */
bool net_wifi_backup(void);
/* The Ethernet link, whichever interface is live (false on a board without Ethernet). */
bool net_eth_link_up(void);
/* True while the live interface's link is up (Ethernet link, or associated with the Wi-Fi
 * access point). */
bool net_link_up(void);
/* Called on every link-up (set before net_start). */
void net_on_link_up(void (*cb)(void));
/* Current IPv4 address in network order, 0 until assigned. A static address is set before
 * the link comes up, so this alone doesn't say the node is on the network: net_online. */
uint32_t net_ip(void);
/* The link is up and the node has an address. */
bool net_online(void);
/* Current default gateway in network order, 0 if none. */
uint32_t net_gateway(void);
/* Applies the network settings that change live (Wi-Fi transmit power, power saving; on a
 * static address, the forwarders as the node's own resolver). */
void net_apply_live(void);
/* The live interface's MAC address. */
void net_mac(uint8_t mac[6]);
/* mDNS host name, without .local. */
const char *net_hostname(void);

/* Wi-Fi only. True if a network is set (node config) or saved (Improv). */
bool net_wifi_configured(void);
/* Joins this network, waiting up to timeout_ms for an address (for the association, while
 * Ethernet is live); on success saves it to NVS, on failure goes back to the saved one. On a
 * wired board on Ethernet it starts Wi-Fi for the join and stops it again after. */
bool net_wifi_join(const char *ssid, const char *pass, int timeout_ms);
/* Why the last Wi-Fi disconnect happened (wifi_err_reason_t, 0 = none), and in words. */
uint16_t net_wifi_last_reason(void);
void net_wifi_stats(net_wifi_stats_t *st);
const char *net_wifi_reason(uint16_t reason);
/* Signal strength of the current access point in dBm, 0 if not associated. */
int net_wifi_rssi(void);
/* Scans for networks. Calls cb for each (strongest first); returns how many. On a wired board
 * on Ethernet it starts Wi-Fi for the scan and stops it again after. */
int net_wifi_scan(void (*cb)(const char *ssid, int rssi, bool secure, void *arg), void *arg);
