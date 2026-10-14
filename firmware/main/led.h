/*
 * The status LED in effect (settings()->led: the node config's, else the board definition's,
 * board_led_t): none, a plain GPIO (active high or low), one WS2812 RGB LED driven with the
 * RMT peripheral, or a plain RGB LED on three GPIOs driven with the RMT too. health_node.c
 * decides what it shows, and is the only caller.
 */
#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "board_def.h"

/* Sets up the LED the settings give, stopping the one before. False if there is none or it
 * failed to start (then led_set does nothing). */
bool led_init(void);
/* On in colour r,g,b (a single-colour LED ignores the colour), or off. */
void led_set(bool on, uint8_t r, uint8_t g, uint8_t b);
/* False, with why, if one of l's pins can't drive an LED on this chip (no output, or taken
 * by the flash, the PSRAM or the Ethernet MAC). */
bool led_check(const board_led_t *l, char *why, size_t cap);
/* Appends /status's "led":{...}: kind ("none", "gpio", "ws2812", "rgb"), failed (it didn't
 * start), pins, active_low, from ("config" or "board"), the board's LED, and identify_s. */
size_t led_status_json(char *j, size_t cap, uint32_t identify_s);
