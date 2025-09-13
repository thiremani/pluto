#include <inttypes.h>
#include <math.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include "util.h"

// dup_cstr in util.h provides a portable strdup-like helper.

// Convert a range [s..t) with step p into a NUL-terminated string.
// Caller is responsible for free()ing the returned buffer.
char *range_i64_str(int64_t s, int64_t t, int64_t p) {
    // Reserve enough space: up to 20 digits per number, two colons, plus NUL.
    // 3*21 + 2 = 65 bytes is plenty.
    char *buf = malloc(65);
    if (!buf) return NULL;
    if (p == 1) {
        // omit the default ":1"
        /* bounded print to avoid CRT warnings/overflow */
        snprintf(buf, 65, "%" PRId64 ":%" PRId64, s, t);
    } else {
        /* bounded print to avoid CRT warnings/overflow */
        snprintf(buf, 65, "%" PRId64 ":%" PRId64 ":%" PRId64, s, t, p);
    }
    return buf;
}


/* ---------- portable float formatting ---------- */
/* Canonicalize special values across platforms:
   - NaN  => "NaN"  (no sign)
   - +Inf => "+Inf"
   - -Inf => "-Inf"
   For normal numbers we use round-trip precisions: %.15g for f64, %.9g for f32.
   Caller must free() the returned string. */

char *f64_str(double x) {
    if (isnan(x)) {
        return dup_cstr("NaN");
    }
    if (isinf(x)) {
        return dup_cstr(signbit(x) ? "-Inf" : "+Inf");
    }

    /* Print with round-trip precision */
    char tmp[64];
    int n = snprintf(tmp, sizeof tmp, "%.15g", x);
    if (n < 0 || n >= (int)sizeof(tmp)) return NULL;

    char *out = (char *)malloc((size_t)n + 1);
    if (!out) return NULL;
    memcpy(out, tmp, (size_t)n + 1);
    return out;
}

char *f32_str(float xf) {
    double x = (double)xf; /* format via double routines */
    if (isnan(x)) {
        return dup_cstr("NaN");
    }
    if (isinf(x)) {
        return dup_cstr(signbit(x) ? "-Inf" : "+Inf");
    }

    char tmp[48];
    int n = snprintf(tmp, sizeof tmp, "%.9g", x); /* ~round-trip for 24-bit mantissa */
    if (n < 0 || n >= (int)sizeof(tmp)) return NULL;

    char *out = (char *)malloc((size_t)n + 1);
    if (!out) return NULL;
    memcpy(out, tmp, (size_t)n + 1);
    return out;
}

/* Enable %n in printf on Windows UCRT (disabled by default for security).
   This matches POSIX behavior relied upon by tests like fmt_ptr. */
#ifdef _WIN32
__declspec(dllimport) int _set_printf_count_output(int);
__attribute__((constructor)) static void pluto_enable_printf_n(void) {
    _set_printf_count_output(1);
}
#endif
