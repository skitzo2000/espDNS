#include "led.h"

#include <stdio.h>

#include "board.h"
#include "driver/gpio.h"
#include "esp_log.h"
#include "esp_private/esp_gpio_reserve.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "settings.h"
#include "soc/soc_caps.h"
#if SOC_RMT_SUPPORTED
#include "driver/rmt_tx.h"
#endif

static const char *TAG = "led";

/* WS2812 at full brightness is glaring and draws ~40 mA: colours are scaled to this. */
#define WS2812_MAX 40

static const char *const KINDS[] = { "none", "gpio", "ws2812", "rgb" };
static board_led_t s_led; /* the one running */
static bool s_ok, s_failed;
#if SOC_RMT_SUPPORTED
/* RMT TX channels: a WS2812's in [0], an RGB LED's red, green, blue; and its encoder. */
static rmt_channel_handle_t s_ch[3];
static rmt_encoder_handle_t s_enc;

/* A channel enabled holds the CPU at its full clock (ESP-IDF's power management lock for
 * the RMT): enabled only while it sends, or the LED would keep the node from ever idling at
 * a lower one. A refused disable is tried again a tick later: the send-done interrupt, on
 * the core the channel was made on, hands the send back (which ends rmt_tx_wait_all_done) a
 * few microseconds before it marks the channel idle, and a disable in between is refused;
 * the channel would stay enabled, holding the full clock, and every later enable be refused. */
static bool chan_off(rmt_channel_handle_t ch)
{
    esp_err_t err = rmt_disable(ch);
    for (int i = 0; err == ESP_ERR_INVALID_STATE && i < 3; i++) {
        vTaskDelay(1);
        err = rmt_disable(ch);
    }
    return err == ESP_OK;
}

/* Enabled for a send; one left enabled (a disable that still failed, a loop) is disabled first. */
static bool chan_on(rmt_channel_handle_t ch)
{
    esp_err_t err = rmt_enable(ch);
    if (err == ESP_ERR_INVALID_STATE && chan_off(ch))
        err = rmt_enable(ch);
    return err == ESP_OK;
}

/* n TX channels on the LED's pins at hz, disabled; inverted for an LED lit low. */
static bool chans_init(int n, uint32_t hz)
{
    esp_err_t err = ESP_OK;
    for (int i = 0; i < n && err == ESP_OK; i++) {
        rmt_tx_channel_config_t cfg = {
            .gpio_num = s_led.pin[i],
            .clk_src = RMT_CLK_SRC_DEFAULT,
            .resolution_hz = hz,
            .mem_block_symbols = SOC_RMT_MEM_WORDS_PER_CHANNEL,
            .trans_queue_depth = 1,
            .flags.invert_out = s_led.kind == BOARD_LED_RGB && s_led.active_low,
        };
        if ((err = rmt_new_tx_channel(&cfg, &s_ch[i])) == ESP_OK && (err = rmt_enable(s_ch[i])) == ESP_OK)
            err = rmt_disable(s_ch[i]);
    }
    if (err != ESP_OK)
        ESP_LOGE(TAG, "%s on GPIO %d: %s", KINDS[s_led.kind], s_led.pin[0], esp_err_to_name(err));
    return err == ESP_OK;
}

/* WS2812 bits at 10 MHz (0.1 us ticks): 0 = 0.3 us high, 0.9 us low; 1 = 0.9 high, 0.3 low.
 * The line idles low between frames, which is the reset (> 50 us). */
static bool ws2812_init(void)
{
    rmt_bytes_encoder_config_t enc = {
        .bit0 = { .level0 = 1, .duration0 = 3, .level1 = 0, .duration1 = 9 },
        .bit1 = { .level0 = 1, .duration0 = 9, .level1 = 0, .duration1 = 3 },
        .flags.msb_first = 1,
    };
    return rmt_new_bytes_encoder(&enc, &s_enc) == ESP_OK && chans_init(1, 10 * 1000 * 1000);
}

static void ws2812_set(uint8_t r, uint8_t g, uint8_t b)
{
    /* The RMT reads the buffer after rmt_transmit returns: static, and the send is over (or
     * given up) before the channel is disabled again. 24 bits take 30 us. */
    static uint8_t grb[3];
    grb[0] = (uint8_t)(g * WS2812_MAX / 255);
    grb[1] = (uint8_t)(r * WS2812_MAX / 255);
    grb[2] = (uint8_t)(b * WS2812_MAX / 255);
    if (!chan_on(s_ch[0]))
        return;
    rmt_transmit_config_t tx = { .loop_count = 0 };
    if (rmt_transmit(s_ch[0], s_enc, grb, sizeof(grb), &tx) == ESP_OK)
        rmt_tx_wait_all_done(s_ch[0], 10);
    chan_off(s_ch[0]);
}

/* A plain RGB LED: an RMT channel a colour, 1 us ticks. Off or full is a level: one symbol
 * sent, which the line keeps after it (its end level), and the channel disabled as the
 * WS2812's is. Part way, a 1 kHz PWM symbol sent in a loop, the channel left enabled: that
 * holds the full clock while such a colour is lit (amber's green). The duty is the colour
 * squared, as the eye sees brightness: amber stays amber, not a washed-out yellow. */
#define RGB_PERIOD 1000
static int16_t s_hi[3]; /* each colour's ticks lit a period, as last sent; -1: none sent */

static bool rgb_init(void)
{
    rmt_copy_encoder_config_t enc = {};
    s_hi[0] = s_hi[1] = s_hi[2] = -1;
    return rmt_new_copy_encoder(&enc, &s_enc) == ESP_OK && chans_init(3, 1000 * 1000);
}

static void rgb_set(uint8_t r, uint8_t g, uint8_t b)
{
    static rmt_symbol_word_t sym[3]; /* read by the RMT after rmt_transmit returns */
    const uint8_t c[3] = { r, g, b };
    for (int i = 0; i < 3; i++) {
        int hi = c[i] * c[i] / 255 * RGB_PERIOD / 255;
        if (hi == s_hi[i] || !chan_on(s_ch[i]))
            continue;
        s_hi[i] = (int16_t)hi;
        bool pwm = hi > 0 && hi < RGB_PERIOD;
        sym[i] = (rmt_symbol_word_t){ .level0 = hi > 0, .duration0 = pwm ? hi : 1,
                                      .level1 = hi == RGB_PERIOD, .duration1 = pwm ? RGB_PERIOD - hi : 1 };
        rmt_transmit_config_t tx = { .loop_count = pwm ? -1 : 0, .flags.eot_level = hi == RGB_PERIOD };
        if (rmt_transmit(s_ch[i], s_enc, &sym[i], sizeof(sym[i]), &tx) == ESP_OK && !pwm)
            rmt_tx_wait_all_done(s_ch[i], 10);
        if (!pwm)
            chan_off(s_ch[i]);
    }
}

/* The LED's channels and encoder, deleted (led_set has turned it off: every channel disabled). */
static void chans_del(void)
{
    for (int i = 0; i < 3; i++)
        if (s_ch[i]) {
            rmt_del_channel(s_ch[i]);
            s_ch[i] = NULL;
        }
    if (s_enc)
        rmt_del_encoder(s_enc);
    s_enc = NULL;
}
#endif

/* The LED running stops, its pins left driven at their off level. */
static void led_stop(void)
{
    if (!s_ok)
        return;
    led_set(false, 0, 0, 0);
    s_ok = false;
#if SOC_RMT_SUPPORTED
    chans_del();
#endif
    for (int i = 0; i < 3; i++)
        if (s_led.pin[i] != BOARD_NC) {
            gpio_reset_pin(s_led.pin[i]);
            gpio_set_direction(s_led.pin[i], GPIO_MODE_OUTPUT);
            gpio_set_level(s_led.pin[i], s_led.active_low);
        }
}

bool led_init(void)
{
    led_stop();
    s_led = settings()->led;
    s_failed = false;
    switch (s_led.kind) {
    case BOARD_LED_GPIO:
        s_ok = gpio_reset_pin(s_led.pin[0]) == ESP_OK && gpio_set_direction(s_led.pin[0], GPIO_MODE_OUTPUT) == ESP_OK;
        break;
#if SOC_RMT_SUPPORTED
    case BOARD_LED_WS2812:
        s_ok = ws2812_init();
        break;
    case BOARD_LED_RGB:
        s_ok = rgb_init();
        break;
#endif
    default:
        return false;
    }
    s_failed = !s_ok;
#if SOC_RMT_SUPPORTED
    if (!s_ok)
        chans_del();
#endif
    if (s_ok) {
        led_set(false, 0, 0, 0);
        ESP_LOGI(TAG, "%s on GPIO %d%s", KINDS[s_led.kind], s_led.pin[0], s_led.kind == BOARD_LED_RGB ? " (red)" : "");
    }
    return s_ok;
}

void led_set(bool on, uint8_t r, uint8_t g, uint8_t b)
{
    if (!s_ok)
        return;
    if (s_led.kind == BOARD_LED_GPIO) {
        gpio_set_level(s_led.pin[0], on != s_led.active_low);
        return;
    }
    if (!on)
        r = g = b = 0;
#if SOC_RMT_SUPPORTED
    if (s_led.kind == BOARD_LED_RGB)
        rgb_set(r, g, b);
    else
        ws2812_set(r, g, b);
#endif
}

bool led_check(const board_led_t *l, char *why, size_t cap)
{
    for (int i = 0; i < 3 && l->kind != BOARD_LED_NONE; i++) {
        int p = l->pin[i];
        if (p != BOARD_NC && (!GPIO_IS_VALID_OUTPUT_GPIO(p) || esp_gpio_is_reserved(BIT64(p)))) {
            snprintf(why, cap, "led: GPIO %d %s", p,
                     GPIO_IS_VALID_OUTPUT_GPIO(p) ? "is taken by the flash, PSRAM or Ethernet" : "can't drive an LED on this chip");
            return false;
        }
    }
    return true;
}

/* "pins":[...],"active_low":... */
static size_t pins_json(char *j, size_t cap, const board_led_t *l)
{
    int n = l->kind == BOARD_LED_RGB ? 3 : l->kind != BOARD_LED_NONE;
    size_t k = (size_t)snprintf(j, cap, "\"pins\":[");
    for (int i = 0; i < n && k < cap; i++)
        k += (size_t)snprintf(j + k, cap - k, "%s%d", i ? "," : "", l->pin[i]);
    if (k < cap)
        k += (size_t)snprintf(j + k, cap - k, "],\"active_low\":%s", l->active_low ? "true" : "false");
    return k < cap ? k : cap;
}

size_t led_status_json(char *j, size_t cap, uint32_t identify_s)
{
    const cfg_t *c = settings();
    size_t n = (size_t)snprintf(j, cap, "\"led\":{\"kind\":\"%s\",\"failed\":%s,", KINDS[c->led.kind],
                                s_failed ? "true" : "false");
    if (n < cap)
        n += pins_json(j + n, cap - n, &c->led);
    if (n < cap)
        n += (size_t)snprintf(j + n, cap - n, ",\"from\":\"%s\",\"board\":{\"kind\":\"%s\",",
                              c->led_config ? "config" : "board", KINDS[board.led.kind]);
    if (n < cap)
        n += pins_json(j + n, cap - n, &board.led);
    if (n < cap)
        n += (size_t)snprintf(j + n, cap - n, "},\"identify_s\":%lu},", (unsigned long)identify_s);
    return n < cap ? n : cap - 1;
}
