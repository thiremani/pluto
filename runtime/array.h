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
    NAME*   arr_##SUF##_new(void);                      \
    void    arr_##SUF##_free(NAME* a);                  \
    NAME*   arr_##SUF##_copy(const NAME* a);            \
    size_t  arr_##SUF##_len(const NAME* a);             \
    size_t  arr_##SUF##_cap(const NAME* a);             \
    int     arr_##SUF##_reserve(NAME* a, size_t cap);   \
    int     arr_##SUF##_resize(NAME* a, size_t new_len, T fill); \
    int     arr_##SUF##_push(NAME* a, T v);             \
    int     arr_##SUF##_pop(NAME* a, T* out);           \
    T       arr_##SUF##_get(const NAME* a, size_t i);   \
    void    arr_##SUF##_set(NAME* a, size_t i, T v);    \
    void    arr_##SUF##_swap(NAME* a, size_t i, size_t j); \
    T*      arr_##SUF##_data(NAME* a);

#include "array_types.def"
#undef PT_XNUM

/* ---------- String vector (owning char*) — bespoke (not generated) ---------- */

typedef struct PtArrayStr PtArrayStr;

/* NOTE: This container OWNS its elements:
   - push/set make a deep copy of the incoming C string
   - free/resize(shrink) free removed strings
   - get returns an internal pointer (borrowed); do not free it. */

PtArrayStr*          arr_str_new(void);
void                 arr_str_free(PtArrayStr* a);
PtArrayStr*          arr_str_copy(const PtArrayStr* a);

size_t               arr_str_len(const PtArrayStr* a);
size_t               arr_str_cap(const PtArrayStr* a);

int                  arr_str_reserve(PtArrayStr* a, size_t cap);
int                  arr_str_resize(PtArrayStr* a, size_t new_len);   /* grows with "" */

int                  arr_str_push(PtArrayStr* a, const char* s);
int                  arr_str_push_own(PtArrayStr* a, char* s); /* takes ownership; do NOT free s */
/* returns 0 on ok, and transfers ownership of the popped string to caller (caller must free) */
int                  arr_str_pop(PtArrayStr* a, char** out);

char*                arr_str_get(const PtArrayStr* a, size_t i);  /* caller owns the copy */
const char*          arr_str_borrow(const PtArrayStr* a, size_t i); /* borrowed; do NOT free */
int                  arr_str_set(PtArrayStr* a, size_t i, const char* s); /* 0 on success, -1 on allocation failure */
void                 arr_str_set_own(PtArrayStr* a, size_t i, char* s); /* takes ownership */

void                 arr_str_swap(PtArrayStr* a, size_t i, size_t j);
const char* const*   arr_str_data(const PtArrayStr* a);  /* contiguous, read-only view */

/* -------- Stringification helpers (malloc'ed char*) -------- */
const char* arr_i64_str(const PtArrayI64* a);
const char* arr_f64_str(const PtArrayF64* a);
const char* arr_str_str(const PtArrayStr* a);

#ifdef __cplusplus
} /* extern "C" */
#endif

#endif /* PLUTO_RUNTIME_ARRAY_H */
