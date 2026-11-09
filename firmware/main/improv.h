/*
 * Improv Wi-Fi over the USB serial port (https://www.improv-wifi.com/serial/), so a browser
 * flasher can hand a new node its Wi-Fi network right after flashing: the controller's Install
 * over USB panel speaks this side of it. Runs on every image with a Wi-Fi driver, a wired
 * board on Ethernet too (it starts Wi-Fi for a scan or join and stops it again after, net.h).
 * Packets share the port with the log output, as in the spec.
 */
#pragma once

void improv_start(void);
