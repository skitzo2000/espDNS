/*
 * Improv Wi-Fi over the USB serial port (https://www.improv-wifi.com/serial/), so a browser
 * flasher can hand a new node its Wi-Fi network right after flashing: the controller's Install
 * over USB panel speaks this side of it. Runs only when the node is on Wi-Fi. Packets share
 * the port with the log output, as in the spec.
 */
#pragma once

void improv_start(void);
