#ifndef PLUTO_RUNTIME_UTIL_H
#define PLUTO_RUNTIME_UTIL_H

#include <stdlib.h>
#include <string.h>

/*
 * dup_cstr: malloc-duplicate a C string. Accepts NULL and treats it as "".
 * Returns NULL on allocation failure.
 */
static inline char* dup_cstr(const char* s) {
	if (!s) s = "";
	size_t n = strlen(s) + 1;
	char* p = (char*)malloc(n);
	if (!p) return NULL;
	memcpy(p, s, n);
	return p;
}

/*
 * f64_special_str: Returns a constant string for NaN/Inf values, or NULL for normal numbers.
 * The returned pointer is to a string literal and should NOT be freed.
 */
const char *f64_special_str(double x);

#endif /* PLUTO_RUNTIME_UTIL_H */

