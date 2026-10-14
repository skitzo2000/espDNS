#include "nodekey_node.h"

#include <string.h>

#include "bootloader_random.h"
#include "cfg.h"
#include "esp_log.h"
#include "esp_random.h"
#include "mbedtls/base64.h"
#include "mbedtls/platform_util.h"
#include "nodekey.h"
#include "nvs.h"
#include "ota.h"

static const char *TAG = "nodekey";
#define NS "nodekey"

static char s_pub[4 * ((NODEKEY_PUB_LEN + 2) / 3) + 1];

const char *nodekey_node_pub(void) { return s_pub; }

static int rng(void *ctx, unsigned char *out, size_t n)
{
    esp_fill_random(out, n);
    return 0;
}

static bool read_key(const char *name, uint8_t *out, size_t len)
{
    nvs_handle_t h;
    size_t n = len;
    if (nvs_open(NS, NVS_READONLY, &h) != ESP_OK)
        return false;
    bool ok = nvs_get_blob(h, name, out, &n) == ESP_OK && n == len;
    nvs_close(h);
    return ok;
}

/* Opens wifi.sealed with the private key, read for it alone (cfg_unseal_fn). */
static const char *unseal(const uint8_t *blob, size_t n, char *out, size_t cap)
{
    uint8_t priv[NODEKEY_PRIV_LEN];
    const char *why = read_key("priv", priv, sizeof(priv))
                          ? nodekey_open(priv, ota_trust()->node_id, blob, n, out, cap, rng, NULL)
                          : "this node has no node key";
    mbedtls_platform_zeroize(priv, sizeof(priv));
    return why;
}

void nodekey_node_load(void)
{
    uint8_t priv[NODEKEY_PRIV_LEN], pub[NODEKEY_PUB_LEN];
    bool have = read_key("priv", priv, sizeof(priv)) && read_key("pub", pub, sizeof(pub));
    if (!have) {
        /* True random numbers before the radio is on: the SAR ADC's noise, as the bootloader
         * has it (esp_random.h). */
        bootloader_random_enable();
        have = nodekey_gen(priv, pub, rng, NULL);
        bootloader_random_disable();
        nvs_handle_t h;
        if (have && nvs_open(NS, NVS_READWRITE, &h) == ESP_OK) {
            have = nvs_set_blob(h, "priv", priv, sizeof(priv)) == ESP_OK &&
                   nvs_set_blob(h, "pub", pub, sizeof(pub)) == ESP_OK && nvs_commit(h) == ESP_OK;
            nvs_close(h);
        } else {
            have = false;
        }
        if (have)
            ESP_LOGI(TAG, "made this node's key");
    }
    mbedtls_platform_zeroize(priv, sizeof(priv));
    size_t n = 0;
    if (!have || mbedtls_base64_encode((unsigned char *)s_pub, sizeof(s_pub), &n, pub, sizeof(pub)) != 0) {
        s_pub[0] = 0;
        ESP_LOGE(TAG, "no node key: a config with a Wi-Fi password is refused");
        return;
    }
    cfg_set_unseal(unseal);
}
