# The W5500 driver (MAC RAW over SPI), our own copy of ESP-IDF's (README.md). Built on every
# chip: a board with "ethernet": {"kind": "w5500"} uses it (main/board.c).
idf_component_register(SRCS "esp_eth_mac_w5500.c" "esp_eth_phy_w5500.c"
                       INCLUDE_DIRS "include"
                       REQUIRES esp_eth esp_driver_spi
                       PRIV_REQUIRES log esp_timer esp_driver_gpio esp_hw_support)
