/*
 * SPDX-FileCopyrightText: 2020-2025 Espressif Systems (Shanghai) CO LTD
 *
 * SPDX-License-Identifier: Apache-2.0
 *
 * Changed by espDNS from ESP-IDF v5.5.5's (README.md): the changes are marked "espdns:".
 */
#include <string.h>
#include <stdlib.h>
#include <sys/cdefs.h>
#include <inttypes.h>
#include "w5500_eth.h" /* espdns: was esp_eth_mac_spi.h (README.md) */
#include "driver/gpio.h"
#include "esp_private/gpio.h"
#include "soc/io_mux_reg.h"
#include "driver/spi_master.h"
#include "esp_attr.h"
#include "esp_log.h"
#include "esp_check.h"
#include "esp_timer.h"
#include "esp_system.h"
#include "esp_intr_alloc.h"
#include "esp_heap_caps.h"
#include "esp_cpu.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "freertos/semphr.h"
#include "w5500.h"
#include "w5500_ring.h"
#include "sdkconfig.h"

static const char *TAG = "w5500.mac";

#define W5500_SPI_LOCK_TIMEOUT_MS (50)
#define W5500_TX_MEM_SIZE (0x4000)
#define W5500_RX_MEM_SIZE (0x4000)
#define W5500_100M_TX_TMO_US (200)
#define W5500_10M_TX_TMO_US (1500)
/* espdns: the RX buffer is read in chunks of up to this many bytes, several frames at a time:
 * a multiple of 4 and at least one whole record (2 + W5500_FRAME_MAX). Less when the SPI bus
 * can't move that much in one transaction (its max_transfer_sz; main/board.c sets 4096): the
 * chunk is cut to what it can, and the MAC refused below one whole record. A 4 KB read holds
 * the SPI lock for 1.6 ms at 20 MHz (tiT's next frame waits that long at worst). */
#define W5500_RX_CHUNK (4096)
#define W5500_RX_CHUNK_MIN ((W5500_REC_HDR + W5500_FRAME_MAX + 3) & ~3)
#define W5500_RX_REGS_LEN (8) /* Sn_RX_RSR, Sn_RX_RD, Sn_RX_WR and 2 bytes past, a DMA-sized read */
_Static_assert(W5500_RX_CHUNK % 4 == 0 && W5500_RX_CHUNK >= W5500_RX_CHUNK_MIN, "W5500_RX_CHUNK");
/* espdns: a command (RECV from the receive task, SEND from tiT) written within this long of one
 * the other task wrote first waits for Sn_CR to show that one taken, up to W5500_CMD_TAKE_US,
 * so neither overwrites the other before the chip has it. */
#define W5500_CMD_GUARD_US (100)
#define W5500_CMD_TAKE_US (1000)
#define W5500_TX_BUF ((ETH_MAX_PACKET_SIZE + 3) & ~3) /* the frame being sent, DMA-capable */

typedef struct {
    spi_device_handle_t hdl;
    SemaphoreHandle_t lock;
} eth_spi_info_t;

typedef struct {
    void *ctx;
    void *(*init)(const void *spi_config);
    esp_err_t (*deinit)(void *spi_ctx);
    esp_err_t (*read)(void *spi_ctx, uint32_t cmd,uint32_t addr, void *data, uint32_t data_len);
    esp_err_t (*write)(void *spi_ctx, uint32_t cmd, uint32_t addr, const void *data, uint32_t data_len);
} eth_spi_custom_driver_t;

typedef struct {
    esp_eth_mac_t parent;
    esp_eth_mediator_t *eth;
    eth_spi_custom_driver_t spi;
    TaskHandle_t rx_task_hdl;
    uint32_t sw_reset_timeout_ms;
    int int_gpio_num;
    esp_timer_handle_t poll_timer;
    uint32_t poll_period_ms;
    uint8_t addr[ETH_ADDR_LEN];
    uint8_t *rx_buffer;  /* W5500_RX_CHUNK bytes, DMA-capable: the RX buffer's frames, a chunk at a time */
    uint8_t *rx_regs;    /* W5500_RX_REGS_LEN bytes, DMA-capable */
    uint32_t rx_chunk;   /* bytes per chunk read: W5500_RX_CHUNK, or less if the bus can't */
    /* espdns: rx_lock: the receive task's drain against OPEN and CLOSE (start, stop), which set
     * the RX pointers afresh. running: the socket is open, so the receive task may use the SPI;
     * never before the first start: a board whose optional W5500 isn't fitted deletes the MAC
     * after a failed init, and the task must not be on the bus then. cmd_lock, cmd_task,
     * cmd_us: the last command written, by whom and when (w5500_issue_command). */
    SemaphoreHandle_t rx_lock;
    volatile bool running;
    SemaphoreHandle_t cmd_lock;
    TaskHandle_t cmd_task;
    int64_t cmd_us;
    uint8_t mcast_cnt;
    uint32_t tx_tmo;
    /* espdns: the TX side, under tx_lock. Sn_TX_WR is only ever moved by us, so it is kept
     * here (tx_wr) instead of read per frame; read again after OPEN or an SPI error (tx_resync).
     * tx_check_free: a frame went unconfirmed, so the next one checks for room (Sn_TX_FSR).
     * tx_pending: a SEND was issued at tx_sent_us and not seen through yet. */
    SemaphoreHandle_t tx_lock;
    uint8_t *tx_buffer;  /* W5500_TX_BUF bytes, DMA-capable */
    uint16_t tx_wr;
    bool tx_resync;
    bool tx_check_free;
    bool tx_pending;
    int64_t tx_sent_us;
} emac_w5500_t;

static void *w5500_spi_init(const void *spi_config)
{
    void *ret = NULL;
    eth_w5500_config_t *w5500_config = (eth_w5500_config_t *)spi_config;
    eth_spi_info_t *spi = calloc(1, sizeof(eth_spi_info_t));
    ESP_GOTO_ON_FALSE(spi, NULL, err, TAG, "no memory for SPI context data");

    /* SPI device init */
    spi_device_interface_config_t spi_devcfg;
    spi_devcfg = *(w5500_config->spi_devcfg);
    if (w5500_config->spi_devcfg->command_bits == 0 && w5500_config->spi_devcfg->address_bits == 0) {
        /* configure default SPI frame format */
        spi_devcfg.command_bits = 16; // Actually it's the address phase in W5500 SPI frame
        spi_devcfg.address_bits = 8;  // Actually it's the control phase in W5500 SPI frame
    } else {
        ESP_GOTO_ON_FALSE(w5500_config->spi_devcfg->command_bits == 16 && w5500_config->spi_devcfg->address_bits == 8,
                            NULL, err, TAG, "incorrect SPI frame format (command_bits/address_bits)");
    }
    ESP_GOTO_ON_FALSE(spi_bus_add_device(w5500_config->spi_host_id, &spi_devcfg, &spi->hdl) == ESP_OK, NULL,
                                            err, TAG, "adding device to SPI host #%i failed", w5500_config->spi_host_id + 1);
    /* create mutex */
    spi->lock = xSemaphoreCreateMutex();
    ESP_GOTO_ON_FALSE(spi->lock, NULL, err, TAG, "create lock failed");

    ret = spi;
    return ret;
err:
    if (spi) {
        if (spi->lock) {
            vSemaphoreDelete(spi->lock);
        }
        free(spi);
    }
    return ret;
}

static esp_err_t w5500_spi_deinit(void *spi_ctx)
{
    esp_err_t ret = ESP_OK;
    eth_spi_info_t *spi = (eth_spi_info_t *)spi_ctx;

    spi_bus_remove_device(spi->hdl);
    vSemaphoreDelete(spi->lock);

    free(spi);
    return ret;
}

static inline bool w5500_spi_lock(eth_spi_info_t *spi)
{
    return xSemaphoreTake(spi->lock, pdMS_TO_TICKS(W5500_SPI_LOCK_TIMEOUT_MS)) == pdTRUE;
}

static inline bool w5500_spi_unlock(eth_spi_info_t *spi)
{
    return xSemaphoreGive(spi->lock) == pdTRUE;
}

static esp_err_t w5500_spi_write(void *spi_ctx, uint32_t cmd, uint32_t addr, const void *value, uint32_t len)
{
    esp_err_t ret = ESP_OK;
    eth_spi_info_t *spi = (eth_spi_info_t *)spi_ctx;

    spi_transaction_t trans = {
        .cmd = cmd,
        .addr = addr,
        .length = 8 * len,
        .tx_buffer = value
    };
    if (w5500_spi_lock(spi)) {
        if (spi_device_polling_transmit(spi->hdl, &trans) != ESP_OK) {
            ESP_LOGE(TAG, "%s(%d): spi transmit failed", __FUNCTION__, __LINE__);
            ret = ESP_FAIL;
        }
        w5500_spi_unlock(spi);
    } else {
        ret = ESP_ERR_TIMEOUT;
    }
    return ret;
}

static esp_err_t w5500_spi_read(void *spi_ctx, uint32_t cmd, uint32_t addr, void *value, uint32_t len)
{
    esp_err_t ret = ESP_OK;
    eth_spi_info_t *spi = (eth_spi_info_t *)spi_ctx;

    spi_transaction_t trans = {
        .flags = len <= 4 ? SPI_TRANS_USE_RXDATA : 0, // use direct reads for registers to prevent overwrites by 4-byte boundary writes
        .cmd = cmd,
        .addr = addr,
        .length = 8 * len,
        .rx_buffer = value
    };
    if (w5500_spi_lock(spi)) {
        if (spi_device_polling_transmit(spi->hdl, &trans) != ESP_OK) {
            ESP_LOGE(TAG, "%s(%d): spi transmit failed", __FUNCTION__, __LINE__);
            ret = ESP_FAIL;
        }
        w5500_spi_unlock(spi);
    } else {
        ret = ESP_ERR_TIMEOUT;
    }
    if ((trans.flags & SPI_TRANS_USE_RXDATA) && len <= 4) {
        memcpy(value, trans.rx_data, len);  // copy register values to output
    }
    return ret;
}

static esp_err_t w5500_read(emac_w5500_t *emac, uint32_t address, void *data, uint32_t len)
{
    uint32_t cmd = (address >> W5500_ADDR_OFFSET); // Actually it's the address phase in W5500 SPI frame
    uint32_t addr = ((address & 0xFFFF) | (W5500_ACCESS_MODE_READ << W5500_RWB_OFFSET)
                    | W5500_SPI_OP_MODE_VDM); // Actually it's the command phase in W5500 SPI frame

    return emac->spi.read(emac->spi.ctx, cmd, addr, data, len);
}

static esp_err_t w5500_write(emac_w5500_t *emac, uint32_t address, const void *data, uint32_t len)
{
    uint32_t cmd = (address >> W5500_ADDR_OFFSET); // Actually it's the address phase in W5500 SPI frame
    uint32_t addr = ((address & 0xFFFF) | (W5500_ACCESS_MODE_WRITE << W5500_RWB_OFFSET)
                    | W5500_SPI_OP_MODE_VDM); // Actually it's the command phase in W5500 SPI frame

    return emac->spi.write(emac->spi.ctx, cmd, addr, data, len);
}

static esp_err_t w5500_issue_command(emac_w5500_t *emac, uint8_t command);

static esp_err_t w5500_send_command(emac_w5500_t *emac, uint8_t command, uint32_t timeout_ms)
{
    esp_err_t ret = ESP_OK;
    /* espdns: written by w5500_issue_command, which keeps it from overwriting a command not yet taken */
    ESP_GOTO_ON_ERROR(w5500_issue_command(emac, command), err, TAG, "write SCR failed");
    // after W5500 accepts the command, the command register will be cleared automatically
    uint32_t to = 0;
    for (to = 0; to < timeout_ms / 10; to++) {
        ESP_GOTO_ON_ERROR(w5500_read(emac, W5500_REG_SOCK_CR(0), &command, sizeof(command)), err, TAG, "read SCR failed");
        if (!command) {
            break;
        }
        vTaskDelay(pdMS_TO_TICKS(10));
    }
    ESP_GOTO_ON_FALSE(to < timeout_ms / 10, ESP_ERR_TIMEOUT, err, TAG, "send command timeout");

err:
    return ret;
}

/* espdns: a command without polling Sn_CR until the chip has taken it (back to 0). Nothing
 * after RECV depends on its having been taken: the next look at the RX registers shows RSR
 * caught up with RD, or w5500_rx_regs_read reads them again (and, past that, sends RECV
 * again); nor after SEND: the next frame reads Sn_TX_RD. OPEN and CLOSE still wait
 * (w5500_send_command).
 * What can't be left to chance is two tasks' commands back to back: Sn_CR holds one, so a SEND
 * written before the chip took the RECV just before it (or the other way round) would replace
 * it. So a command that follows another task's within W5500_CMD_GUARD_US first reads Sn_CR
 * until that one is taken: one read, and only then. One task's own commands are further apart
 * than that, and OPEN and CLOSE keep the other two out (rx_lock, tx_lock) while they wait. */
static esp_err_t w5500_issue_command(emac_w5500_t *emac, uint8_t command)
{
    esp_err_t ret = ESP_OK;
    TaskHandle_t me = xTaskGetCurrentTaskHandle();
    ESP_RETURN_ON_FALSE(xSemaphoreTake(emac->cmd_lock, pdMS_TO_TICKS(W5500_SPI_LOCK_TIMEOUT_MS)) == pdTRUE,
                        ESP_ERR_TIMEOUT, TAG, "command lock timeout");
    int64_t now = esp_timer_get_time();
    if (emac->cmd_task != me && now - emac->cmd_us < W5500_CMD_GUARD_US) {
        uint8_t cr = 0;
        do {
            ESP_GOTO_ON_ERROR(w5500_read(emac, W5500_REG_SOCK_CR(0), &cr, sizeof(cr)), out, TAG, "read SCR failed");
        } while (cr != 0 && esp_timer_get_time() - now < W5500_CMD_TAKE_US);
        ESP_GOTO_ON_FALSE(cr == 0, ESP_ERR_TIMEOUT, out, TAG, "command 0x%02x not taken", cr);
    }
    ret = w5500_write(emac, W5500_REG_SOCK_CR(0), &command, sizeof(command));
    emac->cmd_task = me;
    emac->cmd_us = esp_timer_get_time();
out:
    xSemaphoreGive(emac->cmd_lock);
    return ret;
}

static esp_err_t w5500_get_tx_free_size(emac_w5500_t *emac, uint16_t *size)
{
    esp_err_t ret = ESP_OK;
    uint16_t free0, free1 = 0;
    // read TX_FSR register more than once, until we get the same value
    // this is a trick because we might be interrupted between reading the high/low part of the TX_FSR register (16 bits in length)
    do {
        ESP_GOTO_ON_ERROR(w5500_read(emac, W5500_REG_SOCK_TX_FSR(0), &free0, sizeof(free0)), err, TAG, "read TX FSR failed");
        ESP_GOTO_ON_ERROR(w5500_read(emac, W5500_REG_SOCK_TX_FSR(0), &free1, sizeof(free1)), err, TAG, "read TX FSR failed");
    } while (free0 != free1);

    *size = __builtin_bswap16(free0);

err:
    return ret;
}

static esp_err_t w5500_get_rx_received_size(emac_w5500_t *emac, uint16_t *size)
{
    esp_err_t ret = ESP_OK;
    uint16_t received0, received1 = 0;
    do {
        ESP_GOTO_ON_ERROR(w5500_read(emac, W5500_REG_SOCK_RX_RSR(0), &received0, sizeof(received0)), err, TAG, "read RX RSR failed");
        ESP_GOTO_ON_ERROR(w5500_read(emac, W5500_REG_SOCK_RX_RSR(0), &received1, sizeof(received1)), err, TAG, "read RX RSR failed");
    } while (received0 != received1);
    *size = __builtin_bswap16(received0);

err:
    return ret;
}

static esp_err_t w5500_write_buffer(emac_w5500_t *emac, const void *buffer, uint32_t len, uint16_t offset)
{
    esp_err_t ret = ESP_OK;

    ESP_GOTO_ON_ERROR(w5500_write(emac, W5500_MEM_SOCK_TX(0, offset), buffer, len), err, TAG, "write TX buffer failed");
err:
    return ret;
}

static esp_err_t w5500_read_buffer(emac_w5500_t *emac, void *buffer, uint32_t len, uint16_t offset)
{
    esp_err_t ret = ESP_OK;
    ESP_GOTO_ON_ERROR(w5500_read(emac, W5500_MEM_SOCK_RX(0, offset), buffer, len), err, TAG, "read RX buffer failed");
err:
    return ret;
}

static esp_err_t w5500_set_mac_addr(emac_w5500_t *emac)
{
    esp_err_t ret = ESP_OK;
    ESP_GOTO_ON_ERROR(w5500_write(emac, W5500_REG_MAC, emac->addr, 6), err, TAG, "write MAC address register failed");
err:
    return ret;
}

static esp_err_t w5500_reset(emac_w5500_t *emac)
{
    esp_err_t ret = ESP_OK;
    /* software reset */
    uint8_t mr = W5500_MR_RST; // Set RST bit (auto clear)
    ESP_GOTO_ON_ERROR(w5500_write(emac, W5500_REG_MR, &mr, sizeof(mr)), err, TAG, "write MR failed");
    uint32_t to = 0;
    for (to = 0; to < emac->sw_reset_timeout_ms / 10; to++) {
        ESP_GOTO_ON_ERROR(w5500_read(emac, W5500_REG_MR, &mr, sizeof(mr)), err, TAG, "read MR failed");
        if (!(mr & W5500_MR_RST)) {
            break;
        }
        vTaskDelay(pdMS_TO_TICKS(10));
    }
    ESP_GOTO_ON_FALSE(to < emac->sw_reset_timeout_ms / 10, ESP_ERR_TIMEOUT, err, TAG, "reset timeout");

err:
    return ret;
}

static esp_err_t w5500_verify_id(emac_w5500_t *emac)
{
    esp_err_t ret = ESP_OK;
    uint8_t version = 0;

    // W5500 doesn't have chip ID, we check the version number instead
    // The version number may be polled multiple times since it was observed that
    // some W5500 units may return version 0 when it is read right after the reset
    ESP_LOGD(TAG, "Waiting W5500 to start & verify version...");
    uint32_t to = 0;
    for (to = 0; to < emac->sw_reset_timeout_ms / 10; to++) {
        ESP_GOTO_ON_ERROR(w5500_read(emac, W5500_REG_VERSIONR, &version, sizeof(version)), err, TAG, "read VERSIONR failed");
        if (version == W5500_CHIP_VERSION) {
            return ESP_OK;
        }
        vTaskDelay(pdMS_TO_TICKS(10));
    }

    ESP_LOGE(TAG, "W5500 version mismatched, expected 0x%02x, got 0x%02" PRIx8, W5500_CHIP_VERSION, version);
    return ESP_ERR_INVALID_VERSION;
err:
    return ret;
}

static esp_err_t w5500_setup_default(emac_w5500_t *emac)
{
    esp_err_t ret = ESP_OK;
    uint8_t reg_value = 16;

    // Only SOCK0 can be used as MAC RAW mode, so we give the whole buffer (16KB TX and 16KB RX) to SOCK0, which doesn't have any effect for TX though.
    // A larger TX buffer doesn't buy us pipelining - each SEND is one frame and must complete before the next.
    ESP_GOTO_ON_ERROR(w5500_write(emac, W5500_REG_SOCK_RXBUF_SIZE(0), &reg_value, sizeof(reg_value)), err, TAG, "set rx buffer size failed");
    ESP_GOTO_ON_ERROR(w5500_write(emac, W5500_REG_SOCK_TXBUF_SIZE(0), &reg_value, sizeof(reg_value)), err, TAG, "set tx buffer size failed");
    reg_value = 0;
    for (int i = 1; i < 8; i++) {
        ESP_GOTO_ON_ERROR(w5500_write(emac, W5500_REG_SOCK_RXBUF_SIZE(i), &reg_value, sizeof(reg_value)), err, TAG, "set rx buffer size failed");
        ESP_GOTO_ON_ERROR(w5500_write(emac, W5500_REG_SOCK_TXBUF_SIZE(i), &reg_value, sizeof(reg_value)), err, TAG, "set tx buffer size failed");
    }

    /* Enable ping block, disable PPPoE, WOL */
    reg_value = W5500_MR_PB;
    ESP_GOTO_ON_ERROR(w5500_write(emac, W5500_REG_MR, &reg_value, sizeof(reg_value)), err, TAG, "write MR failed");
    /* Disable interrupt for all sockets by default */
    reg_value = 0;
    ESP_GOTO_ON_ERROR(w5500_write(emac, W5500_REG_SIMR, &reg_value, sizeof(reg_value)), err, TAG, "write SIMR failed");
    /* Enable MAC RAW mode for SOCK0, enable MAC filter, no blocking broadcast and block multicast */
    reg_value = W5500_SMR_MAC_RAW | W5500_SMR_MAC_FILTER | W5500_SMR_MAC_BLOCK_MCAST;
    ESP_GOTO_ON_ERROR(w5500_write(emac, W5500_REG_SOCK_MR(0), &reg_value, sizeof(reg_value)), err, TAG, "write SMR failed");
    /* Enable receive event for SOCK0 */
    reg_value = W5500_SIR_RECV;
    ESP_GOTO_ON_ERROR(w5500_write(emac, W5500_REG_SOCK_IMR(0), &reg_value, sizeof(reg_value)), err, TAG, "write SOCK0 IMR failed");
    /* Set the interrupt re-assert level to maximum (~1.5ms) to lower the chances of missing it */
    uint16_t int_level = __builtin_bswap16(0xFFFF);
    ESP_GOTO_ON_ERROR(w5500_write(emac, W5500_REG_INTLEVEL, &int_level, sizeof(int_level)), err, TAG, "write INTLEVEL failed");

err:
    return ret;
}

static esp_err_t emac_w5500_start(esp_eth_mac_t *mac)
{
    esp_err_t ret = ESP_OK;
    emac_w5500_t *emac = __containerof(mac, emac_w5500_t, parent);
    uint8_t reg_value = 0;
    /* espdns: OPEN sets the RX and TX pointers afresh: no drain or frame may straddle it */
    xSemaphoreTake(emac->rx_lock, portMAX_DELAY);
    xSemaphoreTake(emac->tx_lock, portMAX_DELAY);
    /* open SOCK0 */
    ESP_GOTO_ON_ERROR(w5500_send_command(emac, W5500_SCR_OPEN, 100), err, TAG, "issue OPEN command failed");
    /* espdns: the next frame reads Sn_TX_WR again */
    emac->tx_resync = true;
    emac->tx_check_free = false;
    emac->tx_pending = false;
    /* espdns: before the interrupt is on, so that its first wake finds the socket open */
    emac->running = true;
    /* enable interrupt for SOCK0 */
    reg_value = W5500_SIMR_SOCK0;
    ESP_GOTO_ON_ERROR(w5500_write(emac, W5500_REG_SIMR, &reg_value, sizeof(reg_value)), err, TAG, "write SIMR failed");

err:
    if (ret != ESP_OK) {
        emac->running = false; /* espdns: esp_eth keeps the link down: so do we */
    }
    xSemaphoreGive(emac->tx_lock);
    xSemaphoreGive(emac->rx_lock);
    return ret;
}

static esp_err_t emac_w5500_stop(esp_eth_mac_t *mac)
{
    esp_err_t ret = ESP_OK;
    emac_w5500_t *emac = __containerof(mac, emac_w5500_t, parent);
    uint8_t reg_value = 0;
    /* espdns: no drain or frame straddles CLOSE, and the receive task stays off the bus after */
    xSemaphoreTake(emac->rx_lock, portMAX_DELAY);
    xSemaphoreTake(emac->tx_lock, portMAX_DELAY);
    emac->running = false;
    /* disable interrupt */
    ESP_GOTO_ON_ERROR(w5500_write(emac, W5500_REG_SIMR, &reg_value, sizeof(reg_value)), err, TAG, "write SIMR failed");
    /* close SOCK0 */
    ESP_GOTO_ON_ERROR(w5500_send_command(emac, W5500_SCR_CLOSE, 100), err, TAG, "issue CLOSE command failed");

err:
    xSemaphoreGive(emac->tx_lock);
    xSemaphoreGive(emac->rx_lock);
    return ret;
}

static esp_err_t emac_w5500_set_mediator(esp_eth_mac_t *mac, esp_eth_mediator_t *eth)
{
    esp_err_t ret = ESP_OK;
    ESP_GOTO_ON_FALSE(eth, ESP_ERR_INVALID_ARG, err, TAG, "can't set mac's mediator to null");
    emac_w5500_t *emac = __containerof(mac, emac_w5500_t, parent);
    emac->eth = eth;
    return ESP_OK;
err:
    return ret;
}

static esp_err_t emac_w5500_write_phy_reg(esp_eth_mac_t *mac, uint32_t phy_addr, uint32_t phy_reg, uint32_t reg_value)
{
    esp_err_t ret = ESP_OK;
    emac_w5500_t *emac = __containerof(mac, emac_w5500_t, parent);
    // PHY register and MAC registers are mixed together in W5500
    // The only PHY register is PHYCFGR
    ESP_GOTO_ON_FALSE(phy_reg == W5500_REG_PHYCFGR, ESP_FAIL, err, TAG, "wrong PHY register");
    ESP_GOTO_ON_ERROR(w5500_write(emac, W5500_REG_PHYCFGR, &reg_value, sizeof(uint8_t)), err, TAG, "write PHY register failed");

err:
    return ret;
}

static esp_err_t emac_w5500_read_phy_reg(esp_eth_mac_t *mac, uint32_t phy_addr, uint32_t phy_reg, uint32_t *reg_value)
{
    esp_err_t ret = ESP_OK;
    ESP_GOTO_ON_FALSE(reg_value, ESP_ERR_INVALID_ARG, err, TAG, "can't set reg_value to null");
    emac_w5500_t *emac = __containerof(mac, emac_w5500_t, parent);
    // PHY register and MAC registers are mixed together in W5500
    // The only PHY register is PHYCFGR
    ESP_GOTO_ON_FALSE(phy_reg == W5500_REG_PHYCFGR, ESP_FAIL, err, TAG, "wrong PHY register");
    ESP_GOTO_ON_ERROR(w5500_read(emac, W5500_REG_PHYCFGR, reg_value, sizeof(uint8_t)), err, TAG, "read PHY register failed");

err:
    return ret;
}

static esp_err_t emac_w5500_set_addr(esp_eth_mac_t *mac, uint8_t *addr)
{
    esp_err_t ret = ESP_OK;
    ESP_GOTO_ON_FALSE(addr, ESP_ERR_INVALID_ARG, err, TAG, "invalid argument");
    emac_w5500_t *emac = __containerof(mac, emac_w5500_t, parent);
    memcpy(emac->addr, addr, 6);
    ESP_GOTO_ON_ERROR(w5500_set_mac_addr(emac), err, TAG, "set mac address failed");

err:
    return ret;
}

static esp_err_t emac_w5500_get_addr(esp_eth_mac_t *mac, uint8_t *addr)
{
    esp_err_t ret = ESP_OK;
    ESP_GOTO_ON_FALSE(addr, ESP_ERR_INVALID_ARG, err, TAG, "invalid argument");
    emac_w5500_t *emac = __containerof(mac, emac_w5500_t, parent);
    memcpy(addr, emac->addr, 6);

err:
    return ret;
}

static esp_err_t emac_w5500_set_block_ip4_mcast(esp_eth_mac_t *mac, bool block)
{
    esp_err_t ret = ESP_OK;
    emac_w5500_t *emac = __containerof(mac, emac_w5500_t, parent);
    uint8_t smr;
    ESP_GOTO_ON_ERROR(w5500_read(emac, W5500_REG_SOCK_MR(0), &smr, sizeof(smr)), err, TAG, "read SMR failed");
    if (block) {
        smr |= W5500_SMR_MAC_BLOCK_MCAST;
    } else {
        smr &= ~W5500_SMR_MAC_BLOCK_MCAST;
    }
    ESP_GOTO_ON_ERROR(w5500_write(emac, W5500_REG_SOCK_MR(0), &smr, sizeof(smr)), err, TAG, "write SMR failed");
err:
    return ret;
}

static esp_err_t emac_w5500_add_mac_filter(esp_eth_mac_t *mac, uint8_t *addr)
{
    esp_err_t ret = ESP_OK;
    emac_w5500_t *emac = __containerof(mac, emac_w5500_t, parent);
    // W5500 doesn't have specific MAC filter, so we just un-block multicast. W5500 filters out all multicast packets
    // except for IP multicast. However, behavior is not consistent. IPv4 multicast can be blocked, but IPv6 is always
    // accepted (this is not documented behavior, but it's observed on the real hardware).
    if (addr[0] == 0x01 && addr[1] == 0x00 && addr[2] == 0x5e) {
        ESP_GOTO_ON_ERROR(emac_w5500_set_block_ip4_mcast(mac, false), err, TAG, "set block multicast failed");
        emac->mcast_cnt++;
    } else if (addr[0] == 0x33 && addr[1] == 0x33) {
        ESP_LOGW(TAG, "IPv6 multicast is always filtered in by W5500.");
    } else {
        ESP_LOGE(TAG, "W5500 filters in IP multicast frames only!");
        ret = ESP_ERR_NOT_SUPPORTED;
    }
err:
    return ret;
}

static esp_err_t emac_w5500_del_mac_filter(esp_eth_mac_t *mac, uint8_t *addr)
{
    esp_err_t ret = ESP_OK;
    emac_w5500_t *emac = __containerof(mac, emac_w5500_t, parent);

    ESP_GOTO_ON_FALSE(!(addr[0] == 0x33 && addr[1] == 0x33), ESP_FAIL, err, TAG, "IPv6 multicast is always filtered in by W5500.");

    if (addr[0] == 0x01 && addr[1] == 0x00 && addr[2] == 0x5e && emac->mcast_cnt > 0) {
        emac->mcast_cnt--;
    }
    if (emac->mcast_cnt == 0) {
        // W5500 doesn't have specific MAC filter, so we just block multicast
        ESP_GOTO_ON_ERROR(emac_w5500_set_block_ip4_mcast(mac, true), err, TAG, "set block multicast failed");
    }
err:
    return ret;
}

static esp_err_t emac_w5500_set_link(esp_eth_mac_t *mac, eth_link_t link)
{
    esp_err_t ret = ESP_OK;
    emac_w5500_t *emac = __containerof(mac, emac_w5500_t, parent);
    switch (link) {
    case ETH_LINK_UP:
        ESP_LOGD(TAG, "link is up");
        ESP_GOTO_ON_ERROR(mac->start(mac), err, TAG, "w5500 start failed");
        if (emac->poll_timer) {
            ESP_GOTO_ON_ERROR(esp_timer_start_periodic(emac->poll_timer, emac->poll_period_ms * 1000),
                                err, TAG, "start poll timer failed");
        }
        break;
    case ETH_LINK_DOWN:
        ESP_LOGD(TAG, "link is down");
        ESP_GOTO_ON_ERROR(mac->stop(mac), err, TAG, "w5500 stop failed");
        if (emac->poll_timer) {
            ESP_GOTO_ON_ERROR(esp_timer_stop(emac->poll_timer),
                                err, TAG, "stop poll timer failed");
        }
        break;
    default:
        ESP_GOTO_ON_FALSE(false, ESP_ERR_INVALID_ARG, err, TAG, "unknown link status");
        break;
    }

err:
    return ret;
}

static esp_err_t emac_w5500_set_speed(esp_eth_mac_t *mac, eth_speed_t speed)
{
    esp_err_t ret = ESP_OK;
    emac_w5500_t *emac = __containerof(mac, emac_w5500_t, parent);
    switch (speed) {
    case ETH_SPEED_10M:
        emac->tx_tmo = W5500_10M_TX_TMO_US;
        ESP_LOGD(TAG, "working in 10Mbps");
        break;
    case ETH_SPEED_100M:
        emac->tx_tmo = W5500_100M_TX_TMO_US;
        ESP_LOGD(TAG, "working in 100Mbps");
        break;
    default:
        ESP_GOTO_ON_FALSE(false, ESP_ERR_INVALID_ARG, err, TAG, "unknown speed");
        break;
    }

err:
    return ret;
}

static esp_err_t emac_w5500_set_duplex(esp_eth_mac_t *mac, eth_duplex_t duplex)
{
    esp_err_t ret = ESP_OK;
    switch (duplex) {
    case ETH_DUPLEX_HALF:
        ESP_LOGD(TAG, "working in half duplex");
        break;
    case ETH_DUPLEX_FULL:
        ESP_LOGD(TAG, "working in full duplex");
        break;
    default:
        ESP_GOTO_ON_FALSE(false, ESP_ERR_INVALID_ARG, err, TAG, "unknown duplex");
        break;
    }

err:
    return ret;
}

static esp_err_t emac_w5500_set_promiscuous(esp_eth_mac_t *mac, bool enable)
{
    esp_err_t ret = ESP_OK;
    emac_w5500_t *emac = __containerof(mac, emac_w5500_t, parent);
    uint8_t smr = 0;
    ESP_GOTO_ON_ERROR(w5500_read(emac, W5500_REG_SOCK_MR(0), &smr, sizeof(smr)), err, TAG, "read SMR failed");
    if (enable) {
        smr &= ~W5500_SMR_MAC_FILTER;
    } else {
        smr |= W5500_SMR_MAC_FILTER;
    }
    ESP_GOTO_ON_ERROR(w5500_write(emac, W5500_REG_SOCK_MR(0), &smr, sizeof(smr)), err, TAG, "write SMR failed");

err:
    return ret;
}

static esp_err_t emac_w5500_set_all_multicast(esp_eth_mac_t *mac, bool enable)
{
    emac_w5500_t *emac = __containerof(mac, emac_w5500_t, parent);
    ESP_RETURN_ON_ERROR(emac_w5500_set_block_ip4_mcast(mac, !enable), TAG, "set block multicast failed");
    emac->mcast_cnt = 0;
    if (enable) {
        ESP_LOGW(TAG, "W5500 filters in IP multicast frames only!");
    } else {
        ESP_LOGW(TAG, "W5500 always filters in IPv6 multicast frames!");
    }
    return ESP_OK;
}

static esp_err_t emac_w5500_enable_flow_ctrl(esp_eth_mac_t *mac, bool enable)
{
    /* w5500 doesn't support flow control function, so accept any value */
    return ESP_ERR_NOT_SUPPORTED;
}

static esp_err_t emac_w5500_set_peer_pause_ability(esp_eth_mac_t *mac, uint32_t ability)
{
    /* w5500 doesn't support PAUSE function, so accept any value */
    return ESP_ERR_NOT_SUPPORTED;
}

/* espdns: the SEND before this frame's is over once Sn_TX_RD has come up to Sn_TX_WR (the chip
 * moves it there once the data is sent). Read once, and again until tx_tmo has passed since
 * that SEND: the longest a frame takes at the link's speed, past which ESP-IDF's driver gives
 * a frame up and the next goes regardless. The first read mostly says so; tiT reads more only
 * when frames come closer together than a frame's time on the wire. Not seen through: the next
 * frame checks for room (Sn_TX_FSR).
 * (Deciding by the time alone, with no read, once tx_tmo had passed, sent every frame after a
 * pause through the Sn_TX_FSR check, two reads: 5.2 transactions per answer measured, not 3.5.) */
static esp_err_t w5500_tx_wait(emac_w5500_t *emac)
{
    esp_err_t ret = ESP_OK;
    do {
        uint16_t rd = 0;
        ESP_GOTO_ON_ERROR(w5500_read(emac, W5500_REG_SOCK_TX_RD(0), &rd, sizeof(rd)), err, TAG, "read TX RD failed");
        if (__builtin_bswap16(rd) == emac->tx_wr) {
            emac->tx_pending = false;
            return ESP_OK;
        }
    } while (esp_timer_get_time() - emac->tx_sent_us < emac->tx_tmo);
    emac->tx_check_free = true;
    emac->tx_pending = false;
err:
    return ret;
}

/* espdns: a frame out in 4 transactions: Sn_TX_RD, to see the frame before through
 * (w5500_tx_wait), the frame (copied first into a DMA-capable buffer, so the SPI driver
 * copies nothing), Sn_TX_WR and SEND, and back to tiT without waiting for it to go.
 * ESP-IDF's driver also read Sn_TX_FSR twice and Sn_TX_WR, polled Sn_CR after SEND, then
 * polled Sn_IR (with a PHYCFGR read each time) for SEND_OK and cleared it, about 12 in all.
 * Sn_TX_WR is ours to move, so it is kept (tx_wr); the buffer is empty once the frame before
 * has been sent, so its room is checked only after a frame went unseen (tx_check_free).
 * SEND_OK is not used: the receive task clears it with RECV. The link's state is the PHY's
 * polls' business. */
static esp_err_t emac_w5500_transmit(esp_eth_mac_t *mac, uint8_t *buf, uint32_t length)
{
    esp_err_t ret = ESP_OK;
    emac_w5500_t *emac = __containerof(mac, emac_w5500_t, parent);

    ESP_RETURN_ON_FALSE(length <= ETH_MAX_PACKET_SIZE, ESP_ERR_INVALID_ARG,
                        TAG, "frame size is too big (actual %" PRIu32 ", maximum %u)", length, ETH_MAX_PACKET_SIZE);
    ESP_RETURN_ON_FALSE(xSemaphoreTake(emac->tx_lock, pdMS_TO_TICKS(W5500_SPI_LOCK_TIMEOUT_MS)) == pdTRUE,
                        ESP_ERR_TIMEOUT, TAG, "TX lock timeout");
    /* espdns: a frame that waited out a CLOSE (link down) has no socket to go out on */
    ESP_GOTO_ON_FALSE(emac->running, ESP_ERR_INVALID_STATE, out, TAG, "socket closed");
    if (emac->tx_resync) {
        uint16_t wr = 0;
        ESP_GOTO_ON_ERROR(w5500_read(emac, W5500_REG_SOCK_TX_WR(0), &wr, sizeof(wr)), out, TAG, "read TX WR failed");
        emac->tx_wr = __builtin_bswap16(wr);
        emac->tx_resync = false;
    }
    if (emac->tx_pending) {
        ESP_GOTO_ON_ERROR(w5500_tx_wait(emac), out, TAG, "wait for the frame before failed");
    }
    if (emac->tx_check_free) {
        uint16_t free_size = 0;
        ESP_GOTO_ON_ERROR(w5500_get_tx_free_size(emac, &free_size), out, TAG, "get free size failed");
        ESP_GOTO_ON_FALSE(length <= free_size, ESP_ERR_NO_MEM, out, TAG, "free size (%" PRIu16 ") < send length (%" PRIu32 ")", free_size, length);
        emac->tx_check_free = false;
    }
    memcpy(emac->tx_buffer, buf, length);
    ESP_GOTO_ON_ERROR(w5500_write_buffer(emac, emac->tx_buffer, length, emac->tx_wr), resync, TAG, "write frame failed");
    uint16_t wr = emac->tx_wr + length;
    uint16_t wr_be = __builtin_bswap16(wr);
    ESP_GOTO_ON_ERROR(w5500_write(emac, W5500_REG_SOCK_TX_WR(0), &wr_be, sizeof(wr_be)), resync, TAG, "write TX WR failed");
    emac->tx_wr = wr;
    ESP_GOTO_ON_ERROR(w5500_issue_command(emac, W5500_SCR_SEND), resync, TAG, "issue SEND command failed");
    emac->tx_pending = true;
    emac->tx_sent_us = esp_timer_get_time();
    goto out;

resync:
    emac->tx_resync = true;
    emac->tx_check_free = true;
out:
    xSemaphoreGive(emac->tx_lock);
    return ret;
}

/* espdns: the RX registers in one burst (RSR, RD, WR), checked whole against each other
 * (w5500_rx_regs_ok) instead of reading RSR twice and RD on its own: one transaction instead of
 * three. A torn read is read again; when the two still disagree, a RECV the chip missed would
 * leave RSR behind RD, so it gets the RECV again (harmless: RD is unchanged); past that, the
 * datasheet's way (RSR until two reads agree, then RD). */
static esp_err_t w5500_rx_regs_read(emac_w5500_t *emac, w5500_rx_regs_t *r)
{
    esp_err_t ret = ESP_OK;
    for (int i = 0; i < 3; i++) {
        if (i == 2) {
            ESP_GOTO_ON_ERROR(w5500_issue_command(emac, W5500_SCR_RECV), err, TAG, "issue RECV command failed");
        }
        ESP_GOTO_ON_ERROR(w5500_read(emac, W5500_REG_SOCK_RX_RSR(0), emac->rx_regs, W5500_RX_REGS_LEN), err, TAG, "read RX registers failed");
        *r = w5500_rx_regs_parse(emac->rx_regs);
        if (w5500_rx_regs_ok(r)) {
            return ESP_OK;
        }
    }
    uint16_t rsr = 0, rd = 0;
    ESP_GOTO_ON_ERROR(w5500_get_rx_received_size(emac, &rsr), err, TAG, "read RX RSR failed");
    ESP_GOTO_ON_ERROR(w5500_read(emac, W5500_REG_SOCK_RX_RD(0), &rd, sizeof(rd)), err, TAG, "read RX RD failed");
    r->rsr = rsr;
    r->rd = __builtin_bswap16(rd);
    r->wr = r->rd + rsr;
err:
    return ret;
}

/* espdns: frames go to the stack straight from the receive task (emac_w5500_task), several per
 * SPI read; nothing calls this. */
static esp_err_t emac_w5500_receive(esp_eth_mac_t *mac, uint8_t *buf, uint32_t *length)
{
    *length = 0;
    return ESP_ERR_NOT_SUPPORTED;
}

static void w5500_rx_frame(void *ctx, const uint8_t *frame, uint32_t len)
{
    emac_w5500_t *emac = (emac_w5500_t *)ctx;
    uint8_t *buf = malloc(len);
    if (!buf) {
        ESP_LOGE(TAG, "no mem for receive buffer");
        return;
    }
    memcpy(buf, frame, len);
    /* pass the buffer to stack (e.g. TCP/IP layer) */
    emac->eth->stack_input(emac->eth, buf, len);
}

static int w5500_rx_io_read(void *ctx, uint16_t at, uint8_t *buf, uint32_t len)
{
    return w5500_read_buffer((emac_w5500_t *)ctx, buf, len, at) == ESP_OK ? 0 : -1;
}

static int w5500_rx_io_release(void *ctx, uint16_t rd)
{
    emac_w5500_t *emac = (emac_w5500_t *)ctx;
    uint16_t rd_be = __builtin_bswap16(rd);
    if (w5500_write(emac, W5500_REG_SOCK_RX_RD(0), &rd_be, sizeof(rd_be)) != ESP_OK) {
        ESP_LOGE(TAG, "write RX RD failed");
        return -1;
    }
    if (w5500_issue_command(emac, W5500_SCR_RECV) != ESP_OK) {
        ESP_LOGE(TAG, "issue RECV command failed");
        return 1;
    }
    return 0;
}

/* espdns: what is waiting in the RX buffer: the registers (one read), then a chunk at a time
 * (one read: as many whole frames as it holds), RD past them and RECV (w5500_rx_take). Only
 * what the registers showed: the caller cleared the interrupt before, so a frame landing
 * meanwhile raises it again and has the next wake, and a wake's work is bounded by the
 * buffer's 16 KB. (Reading the registers again until they showed nothing cost a read per wake
 * and, when frames landed meanwhile, took them here and then woke for nothing: two more.)
 * false: an SPI error left frames waiting that no interrupt will announce again. */
static bool w5500_rx_drain(emac_w5500_t *emac)
{
    w5500_rx_regs_t r;
    if (w5500_rx_regs_read(emac, &r) != ESP_OK) {
        return false;
    }
    if (r.rsr == 0) {
        return true;
    }
    const w5500_rx_io_t io = {
        .read = w5500_rx_io_read, .release = w5500_rx_io_release, .frame = w5500_rx_frame, .ctx = emac,
    };
    uint16_t bad_at = 0;
    switch (w5500_rx_take(&io, emac->rx_buffer, emac->rx_chunk, r.rd, r.rsr, &bad_at)) {
    case W5500_TAKE_BAD:
        /* out of step with the buffer's records: all that was waiting from there is dropped */
        ESP_LOGE(TAG, "invalid frame length at RX offset %" PRIu16 ": dropped the %" PRIu16 " bytes from there",
                 bad_at, (uint16_t)(r.rd + r.rsr - bad_at));
        return true;
    case W5500_TAKE_IO:
        return false;
    default:
        return true;
    }
}

IRAM_ATTR static void w5500_isr_handler(void *arg)
{
    emac_w5500_t *emac = (emac_w5500_t *)arg;
    BaseType_t high_task_wakeup = pdFALSE;
    /* notify w5500 task */
    vTaskNotifyGiveFromISR(emac->rx_task_hdl, &high_task_wakeup);
    if (high_task_wakeup != pdFALSE) {
        portYIELD_FROM_ISR();
    }
}

static void w5500_poll_timer(void *arg)
{
    emac_w5500_t *emac = (emac_w5500_t *)arg;
    xTaskNotifyGive(emac->rx_task_hdl);
}

static void emac_w5500_task(void *arg)
{
    emac_w5500_t *emac = (emac_w5500_t *)arg;
    uint8_t status = 0;
    while (1) {
        /* check if the task receives any notification */
        if (emac->int_gpio_num >= 0) {                                   // if in interrupt mode
            if (ulTaskNotifyTake(pdTRUE, pdMS_TO_TICKS(1000)) == 0 &&   // if no notification ...
                gpio_get_level(emac->int_gpio_num) != 0) {               // ...and no interrupt asserted
                continue;                                                // -> just continue to check again
            }
        } else {
            ulTaskNotifyTake(pdTRUE, portMAX_DELAY);
        }
        /* espdns: only while the socket is open (start to stop): never on the bus before the
         * MAC has started, so a W5500 that isn't there (an optional add-on, not fitted) is
         * deleted with this task never having touched it. */
        if (!emac->running) {
            continue;
        }
        bool retry = false;
        xSemaphoreTake(emac->rx_lock, portMAX_DELAY);
        if (emac->running) {
            /* espdns: no read of the interrupt status: clear RECV and take what is waiting (the
             * RX registers say). Cleared first, so a frame landing after the look at the
             * registers raises the interrupt again. SEND_OK goes with it, though it never
             * raises the interrupt (Sn_IMR has RECV only): transmit watches Sn_TX_RD instead. */
            status = W5500_SIR_RECV | W5500_SIR_SEND;
            retry = !(w5500_write(emac, W5500_REG_SOCK_IR(0), &status, sizeof(status)) == ESP_OK &&
                      w5500_rx_drain(emac));
        }
        xSemaphoreGive(emac->rx_lock);
        if (retry) {
            /* espdns: an SPI error left frames waiting, and their interrupt has been and gone:
             * try again shortly instead of leaving them to the next frame */
            vTaskDelay(pdMS_TO_TICKS(10));
            xTaskNotifyGive(xTaskGetCurrentTaskHandle());
        }
    }
    vTaskDelete(NULL);
}

static esp_err_t emac_w5500_init(esp_eth_mac_t *mac)
{
    esp_err_t ret = ESP_OK;
    emac_w5500_t *emac = __containerof(mac, emac_w5500_t, parent);
    esp_eth_mediator_t *eth = emac->eth;
    if (emac->int_gpio_num >= 0) {
        gpio_func_sel(emac->int_gpio_num, PIN_FUNC_GPIO);
        gpio_input_enable(emac->int_gpio_num);
        gpio_pullup_en(emac->int_gpio_num);
        gpio_set_intr_type(emac->int_gpio_num, GPIO_INTR_NEGEDGE); // active low
        gpio_intr_enable(emac->int_gpio_num);
        gpio_isr_handler_add(emac->int_gpio_num, w5500_isr_handler, emac);
    }
    ESP_GOTO_ON_ERROR(eth->on_state_changed(eth, ETH_STATE_LLINIT, NULL), err, TAG, "lowlevel init failed");
    /* reset w5500 */
    ESP_GOTO_ON_ERROR(w5500_reset(emac), err, TAG, "reset w5500 failed");
    /* verify chip id */
    ESP_GOTO_ON_ERROR(w5500_verify_id(emac), err, TAG, "verify chip ID failed");
    /* default setup of internal registers */
    ESP_GOTO_ON_ERROR(w5500_setup_default(emac), err, TAG, "w5500 default setup failed");
    return ESP_OK;
err:
    if (emac->int_gpio_num >= 0) {
        gpio_isr_handler_remove(emac->int_gpio_num);
        gpio_reset_pin(emac->int_gpio_num);
    }
    eth->on_state_changed(eth, ETH_STATE_DEINIT, NULL);
    return ret;
}

static esp_err_t emac_w5500_deinit(esp_eth_mac_t *mac)
{
    emac_w5500_t *emac = __containerof(mac, emac_w5500_t, parent);
    esp_eth_mediator_t *eth = emac->eth;
    mac->stop(mac);
    if (emac->int_gpio_num >= 0) {
        gpio_isr_handler_remove(emac->int_gpio_num);
        gpio_reset_pin(emac->int_gpio_num);
    }
    if (emac->poll_timer && esp_timer_is_active(emac->poll_timer)) {
        esp_timer_stop(emac->poll_timer);
    }
    eth->on_state_changed(eth, ETH_STATE_DEINIT, NULL);
    return ESP_OK;
}

static esp_err_t emac_w5500_del(esp_eth_mac_t *mac)
{
    emac_w5500_t *emac = __containerof(mac, emac_w5500_t, parent);
    if (emac->poll_timer) {
        esp_timer_delete(emac->poll_timer);
    }
    vTaskDelete(emac->rx_task_hdl);
    emac->spi.deinit(emac->spi.ctx);
    heap_caps_free(emac->rx_buffer);
    heap_caps_free(emac->rx_regs);
    heap_caps_free(emac->tx_buffer);
    vSemaphoreDelete(emac->tx_lock);
    vSemaphoreDelete(emac->rx_lock);
    vSemaphoreDelete(emac->cmd_lock);
    free(emac);
    return ESP_OK;
}

esp_eth_mac_t *esp_eth_mac_new_w5500(const eth_w5500_config_t *w5500_config, const eth_mac_config_t *mac_config)
{
    esp_eth_mac_t *ret = NULL;
    emac_w5500_t *emac = NULL;
    ESP_GOTO_ON_FALSE(w5500_config && mac_config, NULL, err, TAG, "invalid argument");
    ESP_GOTO_ON_FALSE((w5500_config->int_gpio_num >= 0) != (w5500_config->poll_period_ms > 0), NULL, err, TAG, "invalid configuration argument combination");
    emac = calloc(1, sizeof(emac_w5500_t));
    ESP_GOTO_ON_FALSE(emac, NULL, err, TAG, "no mem for MAC instance");
    /* bind methods and attributes */
    emac->sw_reset_timeout_ms = mac_config->sw_reset_timeout_ms;
    emac->tx_tmo = W5500_10M_TX_TMO_US; /* espdns: the longer, until the link says its speed */
    emac->tx_resync = true;
    emac->int_gpio_num = w5500_config->int_gpio_num;
    emac->poll_period_ms = w5500_config->poll_period_ms;
    emac->parent.set_mediator = emac_w5500_set_mediator;
    emac->parent.init = emac_w5500_init;
    emac->parent.deinit = emac_w5500_deinit;
    emac->parent.start = emac_w5500_start;
    emac->parent.stop = emac_w5500_stop;
    emac->parent.del = emac_w5500_del;
    emac->parent.write_phy_reg = emac_w5500_write_phy_reg;
    emac->parent.read_phy_reg = emac_w5500_read_phy_reg;
    emac->parent.set_addr = emac_w5500_set_addr;
    emac->parent.get_addr = emac_w5500_get_addr;
    emac->parent.add_mac_filter = emac_w5500_add_mac_filter;
    emac->parent.rm_mac_filter = emac_w5500_del_mac_filter;
    emac->parent.set_speed = emac_w5500_set_speed;
    emac->parent.set_duplex = emac_w5500_set_duplex;
    emac->parent.set_link = emac_w5500_set_link;
    emac->parent.set_promiscuous = emac_w5500_set_promiscuous;
    emac->parent.set_all_multicast = emac_w5500_set_all_multicast;
    emac->parent.set_peer_pause_ability = emac_w5500_set_peer_pause_ability;
    emac->parent.enable_flow_ctrl = emac_w5500_enable_flow_ctrl;
    emac->parent.transmit = emac_w5500_transmit;
    emac->parent.receive = emac_w5500_receive;

    if (w5500_config->custom_spi_driver.init != NULL && w5500_config->custom_spi_driver.deinit != NULL
        && w5500_config->custom_spi_driver.read != NULL && w5500_config->custom_spi_driver.write != NULL) {
        ESP_LOGD(TAG, "Using user's custom SPI Driver");
        emac->spi.init = w5500_config->custom_spi_driver.init;
        emac->spi.deinit = w5500_config->custom_spi_driver.deinit;
        emac->spi.read = w5500_config->custom_spi_driver.read;
        emac->spi.write = w5500_config->custom_spi_driver.write;
        /* Custom SPI driver device init */
        ESP_GOTO_ON_FALSE((emac->spi.ctx = emac->spi.init(w5500_config->custom_spi_driver.config)) != NULL, NULL, err, TAG, "SPI initialization failed");
    } else {
        ESP_LOGD(TAG, "Using default SPI Driver");
        emac->spi.init = w5500_spi_init;
        emac->spi.deinit = w5500_spi_deinit;
        emac->spi.read = w5500_spi_read;
        emac->spi.write = w5500_spi_write;
        /* SPI device init */
        ESP_GOTO_ON_FALSE((emac->spi.ctx = emac->spi.init(w5500_config)) != NULL, NULL, err, TAG, "SPI initialization failed");
    }

    /* espdns: the chunk: what one SPI transaction on the bus can move, up to W5500_RX_CHUNK
     * (a custom SPI driver is taken to move W5500_RX_CHUNK) */
    emac->rx_chunk = W5500_RX_CHUNK;
    if (emac->spi.init == w5500_spi_init) {
        size_t max_bytes = 0;
        ESP_GOTO_ON_FALSE(spi_bus_get_max_transaction_len(w5500_config->spi_host_id, &max_bytes) == ESP_OK, NULL, err,
                          TAG, "SPI bus max transfer size unknown");
        if (max_bytes < emac->rx_chunk) {
            emac->rx_chunk = max_bytes & ~3u;
        }
        ESP_GOTO_ON_FALSE(emac->rx_chunk >= W5500_RX_CHUNK_MIN, NULL, err, TAG,
                          "SPI bus max transfer size %u is below one frame (%u)", (unsigned)max_bytes, (unsigned)W5500_RX_CHUNK_MIN);
    }

    /* espdns: buffers and locks before the task that uses them */
    emac->rx_buffer = heap_caps_aligned_alloc(4, W5500_RX_CHUNK, MALLOC_CAP_DMA | MALLOC_CAP_INTERNAL);
    emac->rx_regs = heap_caps_aligned_alloc(4, W5500_RX_REGS_LEN, MALLOC_CAP_DMA | MALLOC_CAP_INTERNAL);
    ESP_GOTO_ON_FALSE(emac->rx_buffer && emac->rx_regs, NULL, err, TAG, "RX buffer allocation failed");
    emac->tx_buffer = heap_caps_aligned_alloc(4, W5500_TX_BUF, MALLOC_CAP_DMA | MALLOC_CAP_INTERNAL);
    emac->tx_lock = xSemaphoreCreateMutex();
    emac->rx_lock = xSemaphoreCreateMutex();
    emac->cmd_lock = xSemaphoreCreateMutex();
    ESP_GOTO_ON_FALSE(emac->tx_buffer && emac->tx_lock && emac->rx_lock && emac->cmd_lock, NULL, err, TAG,
                      "TX buffer allocation failed");

    /* create w5500 task */
    BaseType_t core_num = tskNO_AFFINITY;
    if (mac_config->flags & ETH_MAC_FLAG_PIN_TO_CORE) {
        core_num = esp_cpu_get_core_id();
    }
    BaseType_t xReturned = xTaskCreatePinnedToCore(emac_w5500_task, "w5500_tsk", mac_config->rx_task_stack_size, emac,
                           mac_config->rx_task_prio, &emac->rx_task_hdl, core_num);
    ESP_GOTO_ON_FALSE(xReturned == pdPASS, NULL, err, TAG, "create w5500 task failed");

    if (emac->int_gpio_num < 0) {
        const esp_timer_create_args_t poll_timer_args = {
            .callback = w5500_poll_timer,
            .name = "emac_spi_poll_timer",
            .arg = emac,
            .skip_unhandled_events = true
        };
        ESP_GOTO_ON_FALSE(esp_timer_create(&poll_timer_args, &emac->poll_timer) == ESP_OK, NULL, err, TAG, "create poll timer failed");
    }

    return &(emac->parent);

err:
    if (emac) {
        if (emac->poll_timer) {
            esp_timer_delete(emac->poll_timer);
        }
        if (emac->rx_task_hdl) {
            vTaskDelete(emac->rx_task_hdl);
        }
        if (emac->spi.ctx) {
            emac->spi.deinit(emac->spi.ctx);
        }
        heap_caps_free(emac->rx_buffer);
        heap_caps_free(emac->rx_regs);
        heap_caps_free(emac->tx_buffer);
        if (emac->tx_lock) {
            vSemaphoreDelete(emac->tx_lock);
        }
        if (emac->rx_lock) {
            vSemaphoreDelete(emac->rx_lock);
        }
        if (emac->cmd_lock) {
            vSemaphoreDelete(emac->cmd_lock);
        }
        free(emac);
    }
    return ret;
}
