/*
 * SPDX-FileCopyrightText: 2019-2025 Espressif Systems (Shanghai) CO LTD
 *
 * SPDX-License-Identifier: Apache-2.0
 */
/*
 * The W5500 declarations ESP-IDF 5.5.5 keeps in esp_eth_mac_spi.h and esp_eth_phy.h behind
 * CONFIG_ETH_SPI_ETHERNET_W5500, copied here for this component's own copy of the driver
 * (README.md). That option stays off, so ESP-IDF builds none of its W5500 code and these names
 * are this component's alone.
 */
#pragma once

#include "sdkconfig.h"
#include "esp_eth_mac_spi.h"
#include "esp_eth_phy.h"

#if CONFIG_ETH_SPI_ETHERNET_W5500
#error "CONFIG_ETH_SPI_ETHERNET_W5500 builds ESP-IDF's W5500 driver too: turn it off, components/w5500 is the one used"
#endif
#if !CONFIG_ETH_USE_SPI_ETHERNET
#error "components/w5500 needs CONFIG_ETH_USE_SPI_ETHERNET (the SPI Ethernet driver types)"
#endif

#ifdef __cplusplus
extern "C" {
#endif

/**
 * @brief W5500 specific configuration
 *
 */
typedef struct {
    int int_gpio_num;                                   /*!< Interrupt GPIO number, set -1 to not use interrupt and to poll rx status periodically */
    uint32_t poll_period_ms;                            /*!< Period in ms to poll rx status when interrupt mode is not used */
    spi_host_device_t spi_host_id;                      /*!< SPI peripheral (this field is invalid when custom SPI driver is defined)*/
    spi_device_interface_config_t *spi_devcfg;          /*!< SPI device configuration (this field is invalid when custom SPI driver is defined)*/
    eth_spi_custom_driver_config_t custom_spi_driver;   /*!< Custom SPI driver definitions */
} eth_w5500_config_t;

/**
 * @brief Default W5500 specific configuration
 *
 */
#define ETH_W5500_DEFAULT_CONFIG(spi_host, spi_devcfg_p) \
    {                                           \
        .int_gpio_num = 4,                      \
        .poll_period_ms = 0,                    \
        .spi_host_id = spi_host,                \
        .spi_devcfg = spi_devcfg_p,             \
        .custom_spi_driver = ETH_DEFAULT_SPI,   \
    }

/**
* @brief Create W5500 Ethernet MAC instance
*
* @param w5500_config: W5500 specific configuration
* @param mac_config: Ethernet MAC configuration
*
* @return
*      - instance: create MAC instance successfully
*      - NULL: create MAC instance failed because some error occurred
*/
esp_eth_mac_t *esp_eth_mac_new_w5500(const eth_w5500_config_t *w5500_config, const eth_mac_config_t *mac_config);

/**
* @brief Create a PHY instance of W5500
*
* @param[in] config: configuration of PHY
*
* @return
*      - instance: create PHY instance successfully
*      - NULL: create PHY instance failed because some error occurred
*/
esp_eth_phy_t *esp_eth_phy_new_w5500(const eth_phy_config_t *config);

#ifdef __cplusplus
}
#endif
