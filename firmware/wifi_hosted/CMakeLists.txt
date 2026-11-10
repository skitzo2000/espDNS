# An empty carrier: its idf_component.yml brings esp_wifi_remote + esp_hosted into the
# esp32p4-c6 image. Everything else here works around one file of esp_hosted's own build.
idf_component_register()

# esp_hosted starts itself from a constructor (host/port/esp/freertos/src/
# port_esp_hosted_host_init.c: __attribute__((constructor)) calling esp_hosted_init()),
# which the C runtime runs before app_main - before board_load() can read the board
# partition, so the board's SDIO pins would never reach the transport. Drop that one
# source file (the same set_target_properties(SOURCES ...) trick esp_wifi_remote itself
# uses on the esp_wifi component): board.c then sets the pins with
# esp_hosted_sdio_set_config() and calls esp_hosted_init() itself. esp_hosted_init is
# idempotent (host/api/src/esp_hosted_api.c), and esp_hosted_sdio_set_config refuses once
# the transport is configured - so if the constructor were ever linked again, the attach
# fails loudly instead of running the C6 on guessed pins.
idf_component_get_property(lib espressif__esp_hosted COMPONENT_LIB)
get_target_property(hosted_srcs ${lib} SOURCES)
list(FILTER hosted_srcs EXCLUDE REGEX "port_esp_hosted_host_init\\.c$")
set_target_properties(${lib} PROPERTIES SOURCES "${hosted_srcs}")
message(STATUS "esp_hosted: the startup constructor is out of the build; board.c brings the transport up with the board's pins")
