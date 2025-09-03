#ifndef PLUTO_RUNTIME_ARRAY_H
#define PLUTO_RUNTIME_ARRAY_H

#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* ---------- Numeric vectors (generated) ---------- */
#define PT_XNUM(SUF, T, NAME)                           \
    typedef struct NAME NAME;                           \
    NAME*   pt_##SUF##_new(void);                       \
    void    pt_##SUF##_free(NAME* a);                   \
    size_t  pt_##SUF##_len(const NAME* a);              \
    size_t  pt_##SUF##_cap(const NAME* a);              \
    int     pt_##SUF##_reserve(NAME* a, size_t cap);    \
    int     pt_##SUF##_resize(NAME* a, size_t new_len, T fill); \
    int     pt_##SUF##_push(NAME* a, T v);              \
    int     pt_##SUF##_pop(NAME* a, T* out);            \
    T       pt_##SUF##_get(const NAME* a, size_t i);    \
    int     pt_##SUF##_set(NAME* a, size_t i, T v);     \
    void    pt_##SUF##_swap(NAME* a, size_t i, size_t j); \
    T*      pt_##SUF##_data(NAME* a);

#include "array_types.def"
#undef PT_XNUM

/* ---------- String vector (owning char*) — bespoke (not generated) ---------- */

typedef struct PtArrayStr PtArrayStr;

/* NOTE: This container OWNS its elements:
   - push/set make a deep copy of the incoming C string
   - free/resize(shrink) free removed strings
   - get returns an internal pointer (borrowed); do not free it. */

PtArrayStr*          pt_str_new(void);
void                 pt_str_free(PtArrayStr* a);

size_t               pt_str_len(const PtArrayStr* a);
size_t               pt_str_cap(const PtArrayStr* a);

int                  pt_str_reserve(PtArrayStr* a, size_t cap);
int                  pt_str_resize(PtArrayStr* a, size_t new_len);   /* grows with "" */

int                  pt_str_push(PtArrayStr* a, const char* s);
/* returns 0 on ok, and transfers ownership of the popped string to caller (caller must free) */
int                  pt_str_pop(PtArrayStr* a, char** out);

const char*          pt_str_get(const PtArrayStr* a, size_t i);
int                  pt_str_set(PtArrayStr* a, size_t i, const char* s);

void                 pt_str_swap(PtArrayStr* a, size_t i, size_t j);
const char* const*   pt_str_data(const PtArrayStr* a);  /* contiguous, read-only view */

/* -------- Stringification helpers (malloc'ed char*) -------- */
const char* array_i64_str(const PtArrayI64* a);
const char* array_f64_str(const PtArrayF64* a);
const char* array_str_str(const PtArrayStr* a);

#ifdef __cplusplus
} /* extern "C" */
#endif

#endif /* PLUTO_RUNTIME_ARRAY_H */
