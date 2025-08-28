#include <inttypes.h>
#include <math.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

// Local strdup (ISO C compatible). Define BEFORE use to avoid implicit decls.
static char *dup_cstr(const char *s) {
    size_t n = strlen(s) + 1;
    char *p = (char *)malloc(n);
    if (!p) return NULL;
    memcpy(p, s, n);
    return p;
}

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