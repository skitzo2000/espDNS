#include "nodekey.h"

#include <string.h>

#include "mbedtls/bignum.h"
#include "mbedtls/ecp.h"
#include "mbedtls/gcm.h"
#include "mbedtls/hkdf.h"
#include "mbedtls/md.h"
#include "mbedtls/platform_util.h"

bool nodekey_gen(uint8_t priv[NODEKEY_PRIV_LEN], uint8_t pub[NODEKEY_PUB_LEN], nodekey_rng_t rng, void *ctx)
{
    mbedtls_ecp_group grp;
    mbedtls_ecp_point q;
    mbedtls_mpi d;
    size_t len = 0;
    mbedtls_ecp_group_init(&grp);
    mbedtls_ecp_point_init(&q);
    mbedtls_mpi_init(&d);
    bool ok = mbedtls_ecp_group_load(&grp, MBEDTLS_ECP_DP_SECP256R1) == 0 &&
              mbedtls_ecp_gen_keypair(&grp, &d, &q, rng, ctx) == 0 &&
              mbedtls_mpi_write_binary(&d, priv, NODEKEY_PRIV_LEN) == 0 &&
              mbedtls_ecp_point_write_binary(&grp, &q, MBEDTLS_ECP_PF_UNCOMPRESSED, &len, pub, NODEKEY_PUB_LEN) == 0 &&
              len == NODEKEY_PUB_LEN;
    mbedtls_mpi_free(&d);
    mbedtls_ecp_point_free(&q);
    mbedtls_ecp_group_free(&grp);
    return ok;
}

const char *nodekey_open(const uint8_t priv[NODEKEY_PRIV_LEN], const uint8_t node[6], const uint8_t *blob, size_t n,
                         char *out, size_t cap, nodekey_rng_t rng, void *ctx)
{
    if (n < NODEKEY_PUB_LEN + 8 + NODEKEY_TAG_LEN || n > NODEKEY_PUB_LEN + 63 + NODEKEY_TAG_LEN)
        return "not a sealed password (wrong length)";
    size_t len = n - NODEKEY_PUB_LEN - NODEKEY_TAG_LEN;
    if (cap <= len)
        return "no room for the password";

    /* The shared secret: X of the node key times E. */
    uint8_t z[NODEKEY_PUB_LEN], info[sizeof(NODEKEY_INFO) - 1 + 6], okm[44];
    size_t zlen = 0;
    mbedtls_ecp_group grp;
    mbedtls_ecp_point e, p;
    mbedtls_mpi d;
    mbedtls_ecp_group_init(&grp);
    mbedtls_ecp_point_init(&e);
    mbedtls_ecp_point_init(&p);
    mbedtls_mpi_init(&d);
    bool ok = mbedtls_ecp_group_load(&grp, MBEDTLS_ECP_DP_SECP256R1) == 0 &&
              mbedtls_mpi_read_binary(&d, priv, NODEKEY_PRIV_LEN) == 0 &&
              mbedtls_ecp_point_read_binary(&grp, &e, blob, NODEKEY_PUB_LEN) == 0 &&
              mbedtls_ecp_check_pubkey(&grp, &e) == 0 && mbedtls_ecp_mul(&grp, &p, &d, &e, rng, ctx) == 0 &&
              mbedtls_ecp_point_write_binary(&grp, &p, MBEDTLS_ECP_PF_UNCOMPRESSED, &zlen, z, sizeof(z)) == 0;
    mbedtls_mpi_free(&d);
    mbedtls_ecp_point_free(&p);
    mbedtls_ecp_point_free(&e);
    mbedtls_ecp_group_free(&grp);

    const char *why = ok ? NULL : "not a sealed password (bad key point)";
    if (!why) {
        memcpy(info, NODEKEY_INFO, sizeof(NODEKEY_INFO) - 1);
        memcpy(info + sizeof(NODEKEY_INFO) - 1, node, 6);
        if (mbedtls_hkdf(mbedtls_md_info_from_type(MBEDTLS_MD_SHA256), NULL, 0, z + 1, 32, info, sizeof(info), okm,
                         sizeof(okm)) != 0)
            why = "key derivation failed";
    }
    if (!why) {
        mbedtls_gcm_context g;
        mbedtls_gcm_init(&g);
        if (mbedtls_gcm_setkey(&g, MBEDTLS_CIPHER_ID_AES, okm, 256) != 0 ||
            mbedtls_gcm_auth_decrypt(&g, len, okm + 32, 12, NULL, 0, blob + n - NODEKEY_TAG_LEN, NODEKEY_TAG_LEN,
                                     blob + NODEKEY_PUB_LEN, (unsigned char *)out) != 0)
            why = "does not open with this node's key (sealed for another, or altered)";
        mbedtls_gcm_free(&g);
    }
    mbedtls_platform_zeroize(z, sizeof(z));
    mbedtls_platform_zeroize(okm, sizeof(okm));
    if (!why) {
        out[len] = 0;
        if (strlen(out) != len)
            why = "the password has a NUL byte";
    }
    if (why)
        mbedtls_platform_zeroize(out, cap);
    return why;
}
