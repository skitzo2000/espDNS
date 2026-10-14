/*
 * The node key (portable: needs only mbedTLS): a P-256 key pair a node makes at its first
 * boot, so the controller can send it a Wi-Fi password no one else can read
 * (docs/security.md, The Wi-Fi password). The private key stays in the node's NVS; the
 * public key is /status "node_key" (base64 of the 65-byte point), which the controller pins
 * when it adopts the node (controller internal/pins).
 *
 * A config release never carries the password in plain text: "wifi": {"sealed": "<base64>"}
 * holds it sealed to the node key, the bytes
 *
 *   off  size  field
 *     0    65  the sender's one-time public key E (0x04 || X || Y)
 *    65     n  the password (8-63 bytes), AES-256-GCM encrypted
 *  65+n    16  the GCM tag
 *
 * with the AES key and nonce from HKDF-SHA256 (RFC 5869): input the X coordinate of the
 * ECDH point (node key x E), no salt, info NODEKEY_INFO followed by the 6-byte node ID;
 * 44 bytes out, the key (32) then the nonce (12). No associated data. A fresh E per seal
 * gives every seal its own key and nonce. The blob is kept as it came, in the signed config,
 * and opened each time the config is read. The controller's release.Seal makes the same
 * bytes (tests/nodekey_vectors.json checks both).
 */
#pragma once

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#define NODEKEY_PRIV_LEN 32
#define NODEKEY_PUB_LEN  65
#define NODEKEY_TAG_LEN  16
#define NODEKEY_INFO     "espdns wifi.sealed v1"

/* A random source, as mbedTLS takes one: 0 on success. */
typedef int (*nodekey_rng_t)(void *ctx, unsigned char *out, size_t n);

/* A new key pair. False if it couldn't be made. */
bool nodekey_gen(uint8_t priv[NODEKEY_PRIV_LEN], uint8_t pub[NODEKEY_PUB_LEN], nodekey_rng_t rng, void *ctx);

/* Opens a sealed password (n bytes) for the node with key priv and ID node into out (cap
 * bytes, NUL-terminated). NULL if it opened, else a reason. rng blinds the key's use. */
const char *nodekey_open(const uint8_t priv[NODEKEY_PRIV_LEN], const uint8_t node[6], const uint8_t *blob, size_t n,
                         char *out, size_t cap, nodekey_rng_t rng, void *ctx);
