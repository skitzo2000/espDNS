/*
 * This node's key (nodekey.h): made at the first boot, kept in NVS (namespace "nodekey"),
 * the private key never leaving it: not in /status, not in a log, and in RAM only while a
 * sealed Wi-Fi password is opened (when the config is read: at boot and when one is
 * pushed). /status "node_key" is the public key, base64. A USB install that erases the flash
 * makes a new one at the next boot, which the controller pins when it adopts the node again.
 */
#pragma once

/* Reads the key, or makes and stores one; once, at boot, before the settings load (they open
 * the config's sealed password with it: cfg_set_unseal). */
void nodekey_node_load(void);
/* The public key as /status shows it (base64 of the 65-byte point), "" if there is none. */
const char *nodekey_node_pub(void);
