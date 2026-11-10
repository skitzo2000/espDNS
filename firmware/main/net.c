#include "net.h"

#include <arpa/inet.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "board.h"
#include "boot.h"
#include "esp_app_desc.h"
#include "esp_event.h"
#include "esp_log.h"
#include "esp_mac.h"
#include "esp_netif.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/event_groups.h"
#include "freertos/semphr.h"
#include "freertos/task.h"
#include "mdns.h"
#include "nvs.h"
#include "settings.h"
#if NET_HAS_WIFI
#include "esp_heap_caps.h"
#include "esp_wifi.h"
#include "netsw.h"
#endif

static const char *TAG = "net";

#define WIFI_NVS_NS   "wifi"
#define GOT_IP_BIT    BIT0
#define JOIN_FAIL_BIT BIT1
#define ASSOC_BIT     BIT2

/* The live interface, what the node answers on: every reader below follows it, and on a wired
 * board with a Wi-Fi backup go_live re-points it. */
static volatile net_kind_t s_kind;
static esp_netif_t *volatile s_netif;
static volatile bool s_link;     /* the live interface's link */
static esp_netif_t *s_eth_netif; /* NULL: no Ethernet, or it didn't start */
static volatile bool s_eth_link;
static void (*s_on_link)(void);
static char s_hostname[20];
static bool s_mdns;
static EventGroupHandle_t s_events;
#if NET_HAS_WIFI
static esp_netif_t *volatile s_wifi_netif; /* NULL while Wi-Fi is off */
/* Wi-Fi's interface, made at the first wifi_up and never destroyed: Wi-Fi stops and starts
 * under it. mDNS keeps a pointer to it, and a reader of s_netif may still hold it as a move
 * back to Ethernet is made. */
static esp_netif_t *s_wifi_sta;
static volatile bool s_wifi_link;          /* associated with the access point */
static volatile uint16_t s_last_reason; /* why the last disconnect happened (wifi_err_reason_t) */
static net_wifi_stats_t s_stats;        /* since boot */
static int64_t s_assoc_start;           /* when the current connect attempt began */
static int s_backoff_s = 1;             /* next reconnect delay: 1, 2, 4, 5, 5 ... s */
#endif

net_kind_t net_kind(void) { return s_kind; }
bool net_wifi_backup(void) { return s_eth_netif && s_kind == NET_WIFI; }
bool net_eth_link_up(void) { return s_eth_link; }
bool net_link_up(void) { return s_link; }
bool net_online(void) { return s_link && net_ip(); }
void net_on_link_up(void (*cb)(void)) { s_on_link = cb; }
const char *net_hostname(void) { return s_hostname; }

const char *net_kind_name(void)
{
    switch (s_kind) {
    case NET_ETHERNET: return "ethernet";
    case NET_WIFI: return "wifi";
    default: return "none";
    }
}

uint32_t net_gateway(void)
{
    esp_netif_ip_info_t ip;
    if (!s_netif || esp_netif_get_ip_info(s_netif, &ip) != ESP_OK)
        return 0;
    return ip.gw.addr;
}

uint32_t net_ip(void)
{
    esp_netif_ip_info_t ip;
    if (!s_netif || esp_netif_get_ip_info(s_netif, &ip) != ESP_OK)
        return 0;
    return ip.ip.addr;
}

void net_mac(uint8_t mac[6])
{
    memset(mac, 0, 6);
    if (s_netif)
        esp_netif_get_mac(s_netif, mac);
}

static void link_changed(bool up)
{
    s_link = up;
    if (up) {
        boot_mark(BOOT_LINK);
        ESP_LOGI(TAG, "%s link up (%lld ms)", net_kind_name(), esp_timer_get_time() / 1000);
        if (s_on_link)
            s_on_link();
    } else {
        ESP_LOGW(TAG, "%s link down", net_kind_name());
    }
}

static void ip_event(void *arg, esp_event_base_t base, int32_t id, void *data)
{
    if (id == IP_EVENT_ETH_GOT_IP || id == IP_EVENT_STA_GOT_IP) {
        ip_event_got_ip_t *ev = data;
#if NET_HAS_WIFI
        if (id == IP_EVENT_STA_GOT_IP)
            s_last_reason = 0; /* connected: earlier failed attempts no longer matter */
#endif
        boot_mark(BOOT_IP);
        if (id == IP_EVENT_STA_GOT_IP)
            xEventGroupSetBits(s_events, GOT_IP_BIT); /* what a Wi-Fi join waits for */
        ESP_LOGI(TAG, "ip " IPSTR " (%lld ms)", IP2STR(&ev->ip_info.ip), esp_timer_get_time() / 1000);
    } else if (id == IP_EVENT_STA_LOST_IP) {
        xEventGroupClearBits(s_events, GOT_IP_BIT);
    }
}

#if NET_HAS_WIFI
static esp_timer_handle_t s_switch; /* the pending move between Ethernet and the backup */
static void switch_due(void *arg);

/* The Wi-Fi backup (netsw.h): stops the pending move and starts the one the links ask for,
 * if any, to be made once its hold is over. On every Ethernet link change, at boot (a cable
 * out from power-on sends no event), after a move (the link changed while it was being made)
 * and after a join (a network newly saved). */
static void switch_check(void)
{
    if (!s_eth_netif)
        return;
    esp_timer_stop(s_switch);
    netsw_t next = netsw_next(s_eth_link, s_kind == NET_WIFI, net_wifi_configured());
    if (next != NETSW_STAY)
        esp_timer_start_once(s_switch, (uint64_t)netsw_hold_ms(next) * 1000);
}
#endif

static void eth_event(void *arg, esp_event_base_t base, int32_t id, void *data)
{
    if (id != ETHERNET_EVENT_CONNECTED && id != ETHERNET_EVENT_DISCONNECTED)
        return;
    s_eth_link = id == ETHERNET_EVENT_CONNECTED;
    if (s_kind == NET_ETHERNET)
        link_changed(s_eth_link);
#if NET_HAS_WIFI
    switch_check();
#endif
}

/* The node's own lookups (NTP server names) on a static address, which has no DHCP-given DNS
 * server: the default forwarders, as the node's resolver uses them. Not the node itself over
 * loopback: its own queries would count as the query that confirms a config on trial. */
static void set_resolvers(void)
{
    if (!s_netif || !settings()->ip)
        return;
    uint32_t fwd[CFG_MAX_FWD];
    int timeout_ms;
    int n = settings_forwarders(fwd, &timeout_ms);
    static const esp_netif_dns_type_t TYPE[] = { ESP_NETIF_DNS_MAIN, ESP_NETIF_DNS_BACKUP };
    /* esp_netif refuses an empty one: with one forwarder it is the backup too, so a backup
     * left from a longer list never outlives a live change. */
    for (int i = 0; n > 0 && i < 2; i++) {
        esp_netif_dns_info_t d = { 0 };
        d.ip.type = ESP_IPADDR_TYPE_V4;
        d.ip.u_addr.ip4.addr = fwd[i < n ? i : 0];
        if (esp_netif_set_dns_info(s_netif, TYPE[i], &d) != ESP_OK)
            ESP_LOGW(TAG, "could not set the resolver");
    }
}

/* Stops the DHCP client; already stopped is fine (an interface going live again). */
static void dhcpc_off(esp_netif_t *n)
{
    esp_err_t e = esp_netif_dhcpc_stop(n);
    if (e != ESP_ERR_ESP_NETIF_DHCP_ALREADY_STOPPED)
        ESP_ERROR_CHECK(e);
}

/* The address the settings give: static (the node config's, the board's or the build's), or
 * DHCP only where a setting asks for it. With neither the DHCP client is stopped too, before
 * the interface starts, so not one DHCP packet is sent: on a network with no DHCP server
 * (settings.json's no_dhcp) a node that asks is a node that never gets an address. */
static void apply_address(void)
{
    const cfg_t *c = settings();
    if (c->dhcp && !c->ip) {
        ESP_LOGI(TAG, "address by DHCP (%s setting)", cfg_addr_from(c));
        return;
    }
    if (!c->ip) {
        dhcpc_off(s_netif);
        ESP_LOGE(TAG, "no address: none in the node config, the board definition or the build. "
                      "Flash the node with one from the builder; it sends no DHCP");
        return;
    }
    esp_netif_ip_info_t ip = { 0 };
    ip.ip.addr = c->ip;
    ip.netmask.addr = c->netmask;
    ip.gw.addr = c->gateway;
    dhcpc_off(s_netif);
    ESP_ERROR_CHECK(esp_netif_set_ip_info(s_netif, &ip));
    ESP_LOGI(TAG, "static ip " IPSTR "/" IPSTR " gw " IPSTR " (%s setting)", IP2STR(&ip.ip), IP2STR(&ip.netmask),
             IP2STR(&ip.gw), cfg_addr_from(c));
    set_resolvers();
}

static bool eth_start(void)
{
    esp_eth_handle_t eth;
    if (!board_eth_install(&eth))
        return false;
    esp_netif_config_t cfg = ESP_NETIF_DEFAULT_ETH();
    s_eth_netif = s_netif = esp_netif_new(&cfg);
    apply_address();
    ESP_ERROR_CHECK(esp_netif_attach(s_netif, esp_eth_new_netif_glue(eth)));
#if NET_HAS_WIFI
    /* Off-subnet traffic leaves by the live interface only: Wi-Fi up for a scan or join over
     * USB would otherwise take the default route (go_live moves it). */
    esp_netif_set_default_netif(s_netif);
    esp_timer_create_args_t t = { .callback = switch_due, .name = "net_switch" };
    ESP_ERROR_CHECK(esp_timer_create(&t, &s_switch));
#endif
    s_kind = NET_ETHERNET; /* before the first link event */
    ESP_ERROR_CHECK(esp_event_handler_register(ETH_EVENT, ESP_EVENT_ANY_ID, eth_event, NULL));
    ESP_ERROR_CHECK(esp_eth_start(eth));
#if NET_HAS_WIFI
    switch_check();
#endif
    return true;
}

#if NET_HAS_WIFI
static esp_timer_handle_t s_reconnect;
static volatile bool s_joining; /* net_wifi_join in progress: it handles failures itself */
static char s_last_ssid[33];            /* the network last tried */
static esp_timer_handle_t s_nag;

#define SCAN_MAX       20
#define SCAN_REUSE_US  (60 * 1000 * 1000)

static wifi_ap_record_t s_scan[SCAN_MAX];
static uint16_t s_scan_n;
static int64_t s_scan_at;

const char *net_wifi_reason(uint16_t r)
{
    switch (r) {
    case 0: return "none";
    case WIFI_REASON_AUTH_EXPIRE: return "authentication expired";
    case WIFI_REASON_AUTH_LEAVE: return "access point sent us away";
    case WIFI_REASON_ASSOC_LEAVE: return "left the network";
    case WIFI_REASON_4WAY_HANDSHAKE_TIMEOUT: return "handshake timeout (wrong password?)";
    case WIFI_REASON_MIC_FAILURE: return "wrong password";
    case WIFI_REASON_BEACON_TIMEOUT: return "lost the access point";
    case WIFI_REASON_NO_AP_FOUND: return "network not found (2.4 GHz only)";
    case WIFI_REASON_AUTH_FAIL: return "authentication failed (wrong password?)";
    case WIFI_REASON_ASSOC_FAIL: return "association failed";
    case WIFI_REASON_HANDSHAKE_TIMEOUT: return "handshake timeout (wrong password?)";
    case WIFI_REASON_CONNECTION_FAIL: return "connection failed";
    case WIFI_REASON_NO_AP_FOUND_W_COMPATIBLE_SECURITY: return "network found, security not supported";
    case WIFI_REASON_NO_AP_FOUND_IN_AUTHMODE_THRESHOLD: return "network found, security below threshold";
    case WIFI_REASON_NO_AP_FOUND_IN_RSSI_THRESHOLD: return "network found, signal too weak";
    default: return "other";
    }
}

uint16_t net_wifi_last_reason(void) { return s_last_reason; }

void net_wifi_stats(net_wifi_stats_t *st) { *st = s_stats; }

/* Settings every join uses. WPA3 access points too (and mixed WPA2/WPA3), with protected
 * management frames. 802.11k/v stay off on purpose: an access point that steers clients to
 * 5 GHz uses them to ask a client to move, and this 2.4 GHz-only node would drop off. */
static void wifi_config_prepare(wifi_config_t *wc)
{
    wc->sta.sae_pwe_h2e = WPA3_SAE_PWE_BOTH;
    wc->sta.pmf_cfg.capable = true;
    wc->sta.rm_enabled = 0;
    wc->sta.btm_enabled = 0;
}

/* The network: the node config's if it names one, else the one saved over USB (Improv). */
static bool wifi_load(wifi_config_t *wc)
{
    memset(wc, 0, sizeof(*wc));
    const cfg_t *c = settings();
    if (c->wifi_ssid[0]) {
        strlcpy((char *)wc->sta.ssid, c->wifi_ssid, sizeof(wc->sta.ssid));
        strlcpy((char *)wc->sta.password, c->wifi_pass, sizeof(wc->sta.password));
        wifi_config_prepare(wc);
        return true;
    }
    nvs_handle_t h;
    if (nvs_open(WIFI_NVS_NS, NVS_READONLY, &h) != ESP_OK)
        return false;
    size_t n = sizeof(wc->sta.ssid);
    bool ok = nvs_get_str(h, "ssid", (char *)wc->sta.ssid, &n) == ESP_OK && wc->sta.ssid[0];
    n = sizeof(wc->sta.password);
    if (ok && nvs_get_str(h, "pass", (char *)wc->sta.password, &n) != ESP_OK)
        wc->sta.password[0] = 0;
    nvs_close(h);
    wifi_config_prepare(wc);
    return ok;
}

static void wifi_save(const char *ssid, const char *pass)
{
    nvs_handle_t h;
    if (nvs_open(WIFI_NVS_NS, NVS_READWRITE, &h) != ESP_OK)
        return;
    nvs_set_str(h, "ssid", ssid);
    nvs_set_str(h, "pass", pass);
    nvs_commit(h);
    nvs_close(h);
}

bool net_wifi_configured(void)
{
    wifi_config_t wc;
    return wifi_load(&wc);
}

static void reconnect(void *arg)
{
    if (!s_wifi_netif)
        return;
    s_assoc_start = esp_timer_get_time();
    esp_wifi_connect();
}

/* While not on the network, say so every 10 s: a node without a network is otherwise silent. */
static void nag(void *arg)
{
    if (net_online())
        return;
    if (s_wifi_link)
        ESP_LOGW(TAG, "wifi: joined %s, but the node has no address (it sends no DHCP)", s_last_ssid);
    else if (!s_last_ssid[0])
        ESP_LOGW(TAG, "wifi: no network saved; set one over USB (Improv)");
    else
        ESP_LOGW(TAG, "wifi: not connected; last tried %s: %s", s_last_ssid, net_wifi_reason(s_last_reason));
}

/* Wi-Fi's link is the node's only while Wi-Fi is live: a scan or join over USB on a wired
 * board on Ethernet leaves the node's link alone. */
static void wifi_event(void *arg, esp_event_base_t base, int32_t id, void *data)
{
    if (!s_wifi_netif)
        return; /* stopping: what's left of the last association */
    if (id == WIFI_EVENT_STA_CONNECTED) {
        const wifi_event_sta_connected_t *ev = data;
        s_stats.connects++;
        memcpy(s_stats.bssid, ev->bssid, 6);
        s_stats.channel = ev->channel;
        if (s_assoc_start)
            s_stats.last_join_ms = (uint32_t)((esp_timer_get_time() - s_assoc_start) / 1000);
        s_backoff_s = 1;
        s_wifi_link = true;
        xEventGroupSetBits(s_events, ASSOC_BIT);
        if (s_kind == NET_WIFI)
            link_changed(true);
    } else if (id == WIFI_EVENT_STA_DISCONNECTED) {
        const wifi_event_sta_disconnected_t *ev = data;
        s_last_reason = ev->reason;
        if (s_wifi_link)
            s_stats.drops++; /* lost a working link, as opposed to a failed attempt */
        else
            s_stats.failed_attempts++;
        ESP_LOGW(TAG, "wifi: disconnected from %.*s: reason %u, %s", ev->ssid_len, (const char *)ev->ssid,
                 ev->reason, net_wifi_reason(ev->reason));
        s_wifi_link = false;
        if (s_kind == NET_WIFI && s_link)
            link_changed(false);
        xEventGroupClearBits(s_events, GOT_IP_BIT | ASSOC_BIT);
        xEventGroupSetBits(s_events, JOIN_FAIL_BIT);
        /* Keep trying the saved network, unless a join is being tried: soon at first (an access
         * point that steers clients to 5 GHz often refuses the first few 2.4 GHz attempts),
         * then every 5 s. */
        if (!s_joining && net_wifi_configured() && !esp_timer_is_active(s_reconnect)) {
            esp_timer_start_once(s_reconnect, (uint64_t)s_backoff_s * 1000 * 1000);
            s_backoff_s = s_backoff_s >= 4 ? 5 : s_backoff_s * 2;
        }
    }
}

/* The Wi-Fi settings that change live: the transmit power cap (node config, else the
 * board's, else the chip's) and power saving, off unless the config turns it on, because it
 * adds delay to every answer. */
static void wifi_apply(void)
{
    if (!s_wifi_netif)
        return;
    const cfg_t *c = settings();
    esp_wifi_set_ps(c->wifi_power_save ? WIFI_PS_MIN_MODEM : WIFI_PS_NONE);
    int dbm = c->wifi_tx_dbm ? c->wifi_tx_dbm : 20; /* 20: the chip's maximum */
    esp_wifi_set_max_tx_power((int8_t)(dbm * 4));   /* units of 0.25 dBm */
    ESP_LOGI(TAG, "wifi: transmit power cap %d dBm, power saving %s", dbm, c->wifi_power_save ? "on" : "off");
}

/* Starts Wi-Fi, if it isn't on: at boot on a board without Ethernet, or later (a wired board's
 * backup, a scan or a join over USB). It joins nothing and takes no address: the caller does
 * (wifi_join_saved, go_live). It sends no DHCP unless a setting asks for it, not even for a
 * join that doesn't go live. The free internal RAM before and after is what Wi-Fi costs. */
static bool wifi_up(void)
{
    if (s_wifi_netif)
        return true;
    /* On a companion board this is where the C6's SDIO pins reach esp_hosted and its
     * transport starts; on an S3 it does nothing (board.h). A C6 that doesn't answer - one
     * without esp-hosted slave firmware, which the operator flashes on the bench - fails
     * here and leaves the node wired-only, as it is today. */
    if (!board_wifi_companion_attach())
        return false;
    if (!s_wifi_sta)
        s_wifi_sta = esp_netif_create_default_wifi_sta();
    esp_netif_t *n = s_wifi_sta;
    const cfg_t *c = settings();
    if (!(c->dhcp && !c->ip))
        dhcpc_off(n);
    ESP_LOGI(TAG, "wifi: %u bytes of internal RAM free before init",
             (unsigned)heap_caps_get_free_size(MALLOC_CAP_INTERNAL));
    wifi_init_config_t init = WIFI_INIT_CONFIG_DEFAULT();
    if (esp_wifi_init(&init) != ESP_OK) {
        ESP_LOGE(TAG, "wifi: init failed");
        return false;
    }
    esp_wifi_set_storage(WIFI_STORAGE_RAM); /* the network is kept in our own NVS namespace */
    esp_wifi_set_mode(WIFI_MODE_STA);
    if (!s_reconnect) { /* once: Wi-Fi may start and stop many times */
        esp_timer_create_args_t t = { .callback = reconnect, .name = "wifi_retry" };
        esp_timer_create(&t, &s_reconnect);
        esp_timer_create_args_t g = { .callback = nag, .name = "wifi_nag" };
        esp_timer_create(&g, &s_nag);
        ESP_ERROR_CHECK(esp_event_handler_register(WIFI_EVENT, ESP_EVENT_ANY_ID, wifi_event, NULL));
    }
    s_wifi_netif = n;
    if (esp_wifi_start() != ESP_OK) {
        ESP_LOGE(TAG, "wifi: start failed");
        s_wifi_netif = NULL;
        esp_wifi_deinit();
        return false;
    }
    ESP_LOGI(TAG, "wifi: %u bytes of internal RAM free after start",
             (unsigned)heap_caps_get_free_size(MALLOC_CAP_INTERNAL));
    esp_timer_start_periodic(s_nag, 10 * 1000 * 1000);
    wifi_apply();
    return true;
}

/* Stops Wi-Fi and gives its RAM back (a wired board on Ethernet). Its interface stays. */
static void wifi_down(void)
{
    if (!s_wifi_netif)
        return;
    s_wifi_netif = NULL; /* first: the events the stop sends are ignored */
    esp_timer_stop(s_reconnect);
    esp_timer_stop(s_nag);
    esp_wifi_disconnect();
    esp_wifi_stop();
    esp_wifi_deinit();
    s_wifi_link = false;
    s_backoff_s = 1;
    s_scan_n = 0;
    s_scan_at = 0;
    ESP_LOGI(TAG, "wifi: off, %u bytes of internal RAM free", (unsigned)heap_caps_get_free_size(MALLOC_CAP_INTERNAL));
}

/* Joins the network set or saved, if there is one. */
static void wifi_join_saved(void)
{
    wifi_config_t wc;
    if (wifi_load(&wc)) {
        strlcpy(s_last_ssid, (char *)wc.sta.ssid, sizeof(s_last_ssid));
        ESP_LOGI(TAG, "wifi: joining %s", s_last_ssid);
        s_assoc_start = esp_timer_get_time();
        esp_wifi_set_config(WIFI_IF_STA, &wc);
        esp_wifi_connect();
    } else {
        ESP_LOGW(TAG, "wifi: no network saved; set one over USB (Improv)");
    }
}

/* Makes kind the live interface (a wired board's Wi-Fi backup, and back): the address and the
 * resolvers move to it, and the mDNS TXT "net" and every reader of s_netif/s_kind follow. The
 * old interface gives the static address up, so the two never hold it at once. lwIP announces
 * the address from the new one (a gratuitous ARP when the address is set on a link that is up,
 * or when the link comes up), so the gateway and clients learn the new MAC. */
static void go_live(net_kind_t kind)
{
    esp_netif_t *old = s_netif, *n = kind == NET_WIFI ? s_wifi_netif : s_eth_netif;
    if (old && old != n && settings()->ip) {
        esp_netif_ip_info_t none = { 0 };
        esp_netif_set_ip_info(old, &none);
    }
    s_netif = n;
    s_kind = kind;
    apply_address();
    esp_netif_set_default_netif(n);
    if (s_mdns)
        mdns_service_txt_item_set("_espdns", "_tcp", "net", net_kind_name());
    if (kind == NET_WIFI ? s_wifi_link : s_eth_link)
        link_changed(true);
    else
        s_link = false;
}

static SemaphoreHandle_t s_wifi_lock; /* starting or stopping Wi-Fi, a scan, a join, a move */

static void wifi_lock(void) { xSemaphoreTake(s_wifi_lock, portMAX_DELAY); }
static void wifi_unlock(void) { xSemaphoreGive(s_wifi_lock); }

/* The move the hold was for, if the links still ask for it (netsw.h): in a task of its own,
 * since starting Wi-Fi takes a while and the timer task's stack is small. */
static void switch_task(void *arg)
{
    wifi_lock();
    switch (netsw_next(s_eth_link, s_kind == NET_WIFI, net_wifi_configured())) {
    case NETSW_TO_WIFI:
        ESP_LOGW(TAG, "ethernet link down — switching to wifi");
        if (wifi_up()) {
            go_live(NET_WIFI);
            wifi_join_saved();
        }
        break;
    case NETSW_TO_ETH:
        ESP_LOGI(TAG, "ethernet link back — wifi backup off");
        go_live(NET_ETHERNET);
        wifi_down();
        break;
    case NETSW_STAY:
        break;
    }
    switch_check(); /* a link change during the move, or a Wi-Fi start that failed */
    wifi_unlock();
    vTaskDelete(NULL);
}

static void switch_due(void *arg)
{
    if (xTaskCreate(switch_task, "net_switch", 4096, NULL, 5, NULL) != pdPASS) {
        ESP_LOGE(TAG, "no memory to switch interfaces; trying again in 1 s");
        esp_timer_start_once(s_switch, 1000 * 1000);
    }
}

bool net_wifi_join(const char *ssid, const char *pass, int timeout_ms)
{
    wifi_lock();
    bool started = !s_wifi_netif; /* a wired board on Ethernet: Wi-Fi for this join only */
    if (started && !wifi_up()) {
        wifi_unlock();
        return false;
    }
    /* Live, the join waits for the address; otherwise the Wi-Fi interface has none (the node
     * keeps its address on Ethernet) and the association is the join. */
    EventBits_t done = s_kind == NET_WIFI ? GOT_IP_BIT : ASSOC_BIT;
    wifi_config_t wc = { 0 };
    strlcpy((char *)wc.sta.ssid, ssid, sizeof(wc.sta.ssid));
    strlcpy((char *)wc.sta.password, pass, sizeof(wc.sta.password));
    wifi_config_prepare(&wc);
    s_joining = true;
    esp_timer_stop(s_reconnect);
    /* Leave the current network first and let its disconnect event pass, so it isn't taken
     * for this join failing. */
    if (esp_wifi_disconnect() == ESP_OK)
        xEventGroupWaitBits(s_events, JOIN_FAIL_BIT, pdTRUE, pdFALSE, pdMS_TO_TICKS(500));
    xEventGroupClearBits(s_events, GOT_IP_BIT | ASSOC_BIT | JOIN_FAIL_BIT);
    s_last_reason = 0;
    strlcpy(s_last_ssid, ssid, sizeof(s_last_ssid));
    esp_wifi_set_config(WIFI_IF_STA, &wc);
    ESP_LOGI(TAG, "wifi: joining %s", ssid);
    s_assoc_start = esp_timer_get_time();
    esp_wifi_connect();
    /* A failed attempt shows up as a disconnect: try again a second later, until the timeout
     * (an access point may refuse the first try). */
    int64_t end = esp_timer_get_time() + (int64_t)timeout_ms * 1000;
    bool ok = false;
    int tries = 1;
    while (!ok && esp_timer_get_time() < end) {
        EventBits_t b = xEventGroupWaitBits(s_events, done | JOIN_FAIL_BIT, pdTRUE, pdFALSE, pdMS_TO_TICKS(1000));
        if (b & done) {
            ok = true;
        } else if (b & JOIN_FAIL_BIT) {
            vTaskDelay(pdMS_TO_TICKS(1000));
            tries++;
            esp_wifi_connect();
        }
    }
    if (ok) {
        xEventGroupSetBits(s_events, done);
        wifi_save(ssid, pass);
        ESP_LOGI(TAG, "wifi: joined %s on try %d, saved", ssid, tries);
    } else {
        ESP_LOGW(TAG, "wifi: could not join %s after %d tries: %s", ssid, tries, net_wifi_reason(s_last_reason));
        esp_wifi_disconnect();
        wifi_config_t saved;
        if (s_kind == NET_WIFI && wifi_load(&saved)) {
            esp_wifi_set_config(WIFI_IF_STA, &saved);
            esp_wifi_connect();
        }
    }
    s_joining = false;
    if (s_kind != NET_WIFI) /* design (b): Wi-Fi only while the cable is out */
        wifi_down();
    if (ok)
        switch_check(); /* a cable already out: the network just saved is its backup */
    wifi_unlock();
    return ok;
}

int net_wifi_rssi(void)
{
    wifi_ap_record_t ap;
    return s_wifi_netif && esp_wifi_sta_get_ap_info(&ap) == ESP_OK ? ap.rssi : 0;
}

/* Scanning takes the radio off the access point's channel, and frames it sends meanwhile go
 * unacknowledged: scanning every few seconds, as an installer dialog left open on the board
 * does, got the node dropped for "low ACK" and lost ~10% of packets. So while connected, the
 * last results are reused for a minute, and a scan dwells briefly and goes back between
 * channels. */
int net_wifi_scan(void (*cb)(const char *ssid, int rssi, bool secure, void *arg), void *arg)
{
    wifi_lock();
    if (!s_wifi_netif && !wifi_up()) { /* a wired board on Ethernet: Wi-Fi for this scan only */
        wifi_unlock();
        return 0;
    }
    if (!s_wifi_link || !s_scan_at || esp_timer_get_time() - s_scan_at > SCAN_REUSE_US) {
        wifi_scan_config_t sc = { .show_hidden = false };
        if (s_wifi_link) {
            sc.scan_type = WIFI_SCAN_TYPE_ACTIVE;
            sc.scan_time.active.min = 20;
            sc.scan_time.active.max = 50;
            sc.home_chan_dwell_time = 60;
        }
        s_scan_n = 0;
        if (esp_wifi_scan_start(&sc, true) == ESP_OK) {
            s_scan_n = SCAN_MAX;
            if (esp_wifi_scan_get_ap_records(&s_scan_n, s_scan) != ESP_OK) {
                esp_wifi_clear_ap_list();
                s_scan_n = 0;
            }
            s_scan_at = esp_timer_get_time();
        }
    }
    int shown = 0;
    for (int i = 0; i < s_scan_n; i++) { /* already strongest first */
        if (!s_scan[i].ssid[0])
            continue;
        cb((const char *)s_scan[i].ssid, s_scan[i].rssi, s_scan[i].authmode != WIFI_AUTH_OPEN, arg);
        shown++;
    }
    if (s_kind != NET_WIFI) /* design (b): Wi-Fi only while the cable is out */
        wifi_down();
    wifi_unlock();
    return shown;
}
#else
const char *net_wifi_reason(uint16_t r) { return "none"; }
uint16_t net_wifi_last_reason(void) { return 0; }
void net_wifi_stats(net_wifi_stats_t *st) { memset(st, 0, sizeof(*st)); }
bool net_wifi_configured(void) { return false; }
bool net_wifi_join(const char *ssid, const char *pass, int timeout_ms) { return false; }
int net_wifi_rssi(void) { return 0; }
int net_wifi_scan(void (*cb)(const char *, int, bool, void *), void *arg) { return 0; }
#endif

/* The settings that change live: on a static address, the forwarders as the node's own
 * resolver; with Wi-Fi on, its transmit power cap and power saving (wifi_apply). */
void net_apply_live(void)
{
    set_resolvers();
#if NET_HAS_WIFI
    wifi_apply();
#endif
}

static void mdns_start(void)
{
    uint8_t mac[6];
    esp_efuse_mac_get_default(mac);
    snprintf(s_hostname, sizeof(s_hostname), "espdns-%02x%02x%02x", mac[3], mac[4], mac[5]);
    if (mdns_init() != ESP_OK) {
        ESP_LOGE(TAG, "mdns: init failed");
        return;
    }
    mdns_hostname_set(s_hostname);
    mdns_instance_name_set(s_hostname);
    char node[18];
    snprintf(node, sizeof(node), MACSTR, MAC2STR(mac));
    mdns_txt_item_t txt[] = {
        { "board", board.name },
        { "image", board_image() },
        { "node", node },
        { "version", esp_app_get_description()->version },
        { "net", net_kind_name() },
        { "adopted", settings_adopted() ? "1" : "0" },
    };
    mdns_service_add(NULL, "_espdns", "_tcp", 80, txt, sizeof(txt) / sizeof(txt[0]));
    s_mdns = true;
    ESP_LOGI(TAG, "mdns: %s.local, _espdns._tcp", s_hostname);
}

void net_start(void)
{
    ESP_ERROR_CHECK(esp_netif_init());
    ESP_ERROR_CHECK(esp_event_loop_create_default());
    s_events = xEventGroupCreate();
#if NET_HAS_WIFI
    s_wifi_lock = xSemaphoreCreateMutex();
#endif
    ESP_ERROR_CHECK(esp_event_handler_register(IP_EVENT, ESP_EVENT_ANY_ID, ip_event, NULL));

    if (!eth_start()) {
#if NET_HAS_WIFI
        if (wifi_up()) {
            go_live(NET_WIFI);
            wifi_join_saved();
        } else {
            ESP_LOGE(TAG, "no network: Ethernet and Wi-Fi both failed");
        }
#else
        ESP_LOGE(TAG, "no network: Ethernet failed and this board has no Wi-Fi");
#endif
    }
    if (s_kind != NET_NONE)
        mdns_start();
}
