#include <inttypes.h>
#include <stdio.h>
#include <stdlib.h>

// Convert a range [s..t) with step p into a NUL-terminated string.
// Caller is responsible for free()ing the returned buffer.
char *range_i64_str(int64_t s, int64_t t, int64_t p) {
    // Reserve enough space: up to 20 digits per number, two colons, plus NUL.
    // 3*21 + 2 = 65 bytes is plenty.
    char *buf = malloc(65);
    if (!buf) return NULL;
    if (p == 1) {
        // omit the default “:1”
        sprintf(buf, "%" PRId64 ":%" PRId64, s, t);
    } else {
        sprintf(buf, "%" PRId64 ":%" PRId64 ":%" PRId64, s, t, p);
    }
    return buf;
}