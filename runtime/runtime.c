#include <inttypes.h>
#include <math.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include "util.h"

// dup_cstr in util.h provides a portable strdup-like helper.

// Concatenate two strings and return a newly allocated string.
// Caller is responsible for free()ing the returned buffer.
char *str_concat(const char *left, const char *right) {
    if (!left || !right) return NULL;

    size_t left_len = strlen(left);
    size_t right_len = strlen(right);
    size_t total_len = left_len + right_len;

    char *result = (char *)malloc(total_len + 1);
    if (!result) return NULL;

    memcpy(result, left, left_len);
    memcpy(result + left_len, right, right_len + 1);  // +1 to include null terminator

    return result;
}

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

/* Returns a constant string for NaN/Inf values, or NULL for normal numbers.
   The returned pointer is to a string literal and should NOT be freed. */
const char *f64_special_str(double x) {
    if (isnan(x)) {
        return "NaN";
    }
    if (isinf(x)) {
        return signbit(x) ? "-Inf" : "+Inf";
    }
    return NULL;
}

char *f64_str(double x) {
    const char *special = f64_special_str(x);
    if (special) {
        return dup_cstr(special);
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
    const char *special = f64_special_str(x);
    if (special) {
        return dup_cstr(special);
    }

    char tmp[48];
    int n = snprintf(tmp, sizeof tmp, "%.9g", x); /* ~round-trip for 24-bit mantissa */
    if (n < 0 || n >= (int)sizeof(tmp)) return NULL;

    char *out = (char *)malloc((size_t)n + 1);
    if (!out) return NULL;
    memcpy(out, tmp, (size_t)n + 1);
    return out;
}

// Format a string using snprintf with variadic arguments.
// Uses snprintf to determine size, then allocates exact buffer needed.
// Returns a newly allocated string that the caller must free().
// NOTE: Currently, formatted strings are not automatically freed and will
// leak unless explicitly freed by the caller or at program exit.
// TODO: Implement proper string lifetime management with reference counting
// or scope-based cleanup.
char *sprintf_alloc(const char *fmt, ...) {
    va_list args1, args2;
    va_start(args1, fmt);
    va_copy(args2, args1);

    // Determine required buffer size
    int size = vsnprintf(NULL, 0, fmt, args1);
    va_end(args1);

    if (size < 0) {
        va_end(args2);
        return NULL;
    }

    // Allocate exact buffer needed
    char *buf = (char *)malloc((size_t)size + 1);
    if (!buf) {
        va_end(args2);
        return NULL;
    }

    // Format into buffer
    vsnprintf(buf, (size_t)size + 1, fmt, args2);
    va_end(args2);

    return buf;
}

/* Enable %n in printf on Windows UCRT (disabled by default for security).
   This matches POSIX behavior relied upon by tests like fmt_ptr. */
#ifdef _WIN32
__declspec(dllimport) int _set_printf_count_output(int);
__attribute__((constructor)) static void pluto_enable_printf_n(void) {
    _set_printf_count_output(1);
}
#endif
