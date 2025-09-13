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

#endif /* PLUTO_RUNTIME_UTIL_H */

