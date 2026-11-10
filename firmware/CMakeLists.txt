cmake_minimum_required(VERSION 3.19)

# What to build (docs/architecture.md, Boards and images):
#   IMAGE  the chip image, images/<image>/sdkconfig.defaults (esp32s3-octal, esp32p4-rev1, ...)
#   BOARD  optional: a catalog board (BOARDS_DIR/<board>.json) built in as the fallback, for a
#          node whose flash has no `board` partition yet. IMAGE defaults to the board's image.
set(IMAGE "" CACHE STRING "Chip image to build (images/<image>)")
set(BOARD "" CACHE STRING "Catalog board to build in as the fallback (transitional images)")
set(BOARDS_DIR "${CMAKE_CURRENT_LIST_DIR}/../boards" CACHE PATH "Board catalog")

set(fallback_json "")
if(BOARD)
    set(fallback_json "${BOARDS_DIR}/${BOARD}.json")
    if(NOT EXISTS "${fallback_json}")
        message(FATAL_ERROR "unknown board ${BOARD}: no ${fallback_json}")
    endif()
    file(READ "${fallback_json}" board_def)
    string(JSON board_image GET "${board_def}" image)
    if(NOT IMAGE)
        set(IMAGE "${board_image}")
    elseif(NOT IMAGE STREQUAL board_image)
        message(FATAL_ERROR "board ${BOARD} runs image ${board_image}, not ${IMAGE}")
    endif()
endif()
if(NOT IMAGE OR NOT EXISTS "${CMAKE_CURRENT_LIST_DIR}/images/${IMAGE}/sdkconfig.defaults")
    message(FATAL_ERROR "set IMAGE to one of images/<image> (got '${IMAGE}')")
endif()

# Sibling images: the same chip, PSRAM and flash layout, built differently (esp32s3-eth is
# esp32s3-octal with speed bought with internal RAM, for wired boards). A node moves between
# them over the air and keeps what it has: its board partition and its stored signed
# releases name the image it ran when they were written, and the sibling takes them as its
# own (board.c, release.c). The controller has the same pairs (internal/boards, Siblings).
set(sibling_esp32s3-octal esp32s3-eth)
set(sibling_esp32s3-eth esp32s3-octal)
set(sibling_esp32p4-rev1 esp32p4-c6)
set(sibling_esp32p4-c6 esp32p4-rev1)
set(IMAGE_SIBLING "${sibling_${IMAGE}}")

# Wi-Fi through a companion chip, for the P4 boards that carry one: a component directory
# only the esp32p4-c6 image sees (its idf_component.yml brings esp_wifi_remote + esp_hosted,
# and its CMakeLists takes one of esp_hosted's files out of the build). Every other image -
# a P4 board whose file names no companion included - keeps its component set, its lock and
# its bytes: measured, a rev1 image with the directory gated this way is byte-for-byte the
# image built without it (flash.text 729516, flash.rodata 169912, iram0.text 92978,
# dram0.data 13436, dram0.bss 22184; dns2.bin 1006784 both ways).
if(IMAGE STREQUAL "esp32p4-c6")
    list(APPEND EXTRA_COMPONENT_DIRS "${CMAKE_CURRENT_LIST_DIR}/wifi_hosted")
endif()
set(SDKCONFIG_DEFAULTS "sdkconfig.defaults;images/${IMAGE}/sdkconfig.defaults")
# For experiments: one more defaults file on top, e.g. -DEXTRA_SDKCONFIG=build/wpa2-only.defaults
if(EXTRA_SDKCONFIG)
    list(APPEND SDKCONFIG_DEFAULTS "${EXTRA_SDKCONFIG}")
endif()

# The version: the firmware's VERSION, in this directory (version.cmake), as the app
# descriptor's version (what /status, the image header and the controller say), never
# `git describe`. A change to it configures the build again.
include("${CMAKE_CURRENT_LIST_DIR}/version.cmake")
espdns_version("${CMAKE_CURRENT_LIST_DIR}/VERSION" PROJECT_VER)
set_property(DIRECTORY APPEND PROPERTY CMAKE_CONFIGURE_DEPENDS "${CMAKE_CURRENT_LIST_DIR}/VERSION")

include($ENV{IDF_PATH}/tools/cmake/project.cmake)
idf_build_set_property(ESPDNS_IMAGE "${IMAGE}")
idf_build_set_property(ESPDNS_IMAGE_SIBLING "${IMAGE_SIBLING}")
idf_build_set_property(ESPDNS_FALLBACK "${BOARD}")
idf_build_set_property(ESPDNS_FALLBACK_JSON "${fallback_json}")
# The component lock, committed so every checkout builds the same components: one per chip
# image, because two images of one chip can link different components (esp32p4-rev1 and
# esp32p4-c6: the second adds esp_hosted) - and because the component manager records the
# target in it and, for any other, solves again over the network and rewrites it. A build
# only reads it (offline, the source read-only); `make components` writes it. Measured: with
# the lock keyed on the target instead, a rev1 build after a c6 one rewrites
# dependencies.lock.esp32p4, which CI's "Component lock unchanged" step and
# scripts/release/build-firmware.sh both refuse.
idf_build_set_property(DEPENDENCIES_LOCK "${CMAKE_CURRENT_LIST_DIR}/dependencies.lock.${IMAGE}")
project(dns2)
