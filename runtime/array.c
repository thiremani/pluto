#include "array.h"
#include <stdlib.h>
#include <string.h>
#include <stdio.h>
#include <stdarg.h>
#include <limits.h>   /* INT_MAX, SIZE_MAX */

#include "third_party/klib/kvec.h"

#define PT_MAX(a,b) ((a)>(b)?(a):(b))

/* ---------- helpers ---------- */

static char* pt_dup_cstr(const char* s) {
    if (!s) s = "";
    size_t n = strlen(s) + 1;
    char* p = (char*)malloc(n);
    if (!p) return NULL;
    memcpy(p, s, n);
    return p;
}

/* Overflow-safe ensure-capacity for kvec.
   Notes:
   - kvec uses 'int' for n/m; clamp m <= INT_MAX
   - avoid kv_push so we can report OOM explicitly (return -1)
   - never overflow size computations (SIZE_MAX guards) */
#define PT_DEF_ENSURE_CAP(NAME, T)                                       \
static int NAME##_ensure_cap(void* vv, size_t add) {                     \
    typedef kvec_t(T) kv_t;                                             \
    kv_t* v = (kv_t*)vv;                                                \
    /* add may be 0; we only grow */                                     \
    size_t n = v->n;                                                     \
    if (add > SIZE_MAX - n) return -1;                                   \
    size_t need = n + add;                                               \
    size_t curm = v->m;                                                  \
    if (need <= curm) return 0;                                          \
    size_t newm = PT_MAX((size_t)2, curm);                               \
    while (newm < need) {                                                \
        if (newm > SIZE_MAX / 2u) { newm = need; break; }                \
        newm <<= 1;                                                      \
    }                                                                    \
    if (sizeof(T) != 0 && newm > SIZE_MAX / sizeof(T)) return -1;        \
    T* a = (T*)realloc(v->a, newm * sizeof(T));                          \
    if (!a && newm) return -1;                                           \
    v->a = a; v->m = newm;                                               \
    return 0;                                                            \
}

/* ========== Numeric vector generator (all types from vec_types.def) ========== */

#define PT_DEF_VEC_NUMERIC(SUF, T, NAME)                                 \
struct NAME { kvec_t(T) v; };                                            \
PT_DEF_ENSURE_CAP(SUF, T)                                                \
NAME* pt_##SUF##_new(void){                                              \
    NAME* a = (NAME*)calloc(1, sizeof *a);                               \
    if (!a) return NULL;                                                 \
    kv_init(a->v);                                                       \
    return a;                                                            \
}                                                                        \
void pt_##SUF##_free(NAME* a){                                           \
    if (!a) return;                                                      \
    kv_destroy(a->v);                                                    \
    free(a);                                                             \
}                                                                        \
size_t pt_##SUF##_len(const NAME* a){ return a ? (size_t)a->v.n : 0; }   \
size_t pt_##SUF##_cap(const NAME* a){ return a ? (size_t)a->v.m : 0; }   \
int pt_##SUF##_reserve(NAME* a, size_t cap){                             \
    if (!a) return -1;                                                   \
    if (cap <= (size_t)a->v.m) return 0;                                 \
    size_t n = (size_t)a->v.n;                                           \
    return SUF##_ensure_cap(&a->v, cap > n ? (cap - n) : 0);             \
}                                                                        \
int pt_##SUF##_resize(NAME* a, size_t new_len, T fill){                  \
    if (!a) return -1;                                                   \
    size_t n = (size_t)a->v.n;                                           \
    if (new_len <= n) { a->v.n = new_len; return 0; }                    \
    size_t add = new_len - n;                                            \
    if (SUF##_ensure_cap(&a->v, add) != 0) return -1;                    \
    for (size_t i = 0; i < add; ++i) a->v.a[n + i] = fill;               \
    a->v.n = new_len;                                                    \
    return 0;                                                            \
}                                                                        \
int pt_##SUF##_push(NAME* a, T v){                                       \
    if (!a) return -1;                                                   \
    if (SUF##_ensure_cap(&a->v, 1) != 0) return -1;                      \
    a->v.a[a->v.n++] = v;                                                \
    return 0;                                                            \
}                                                                        \
int pt_##SUF##_pop(NAME* a, T* out){                                     \
    if (!a || a->v.n == 0) return -1;                                    \
    T v = a->v.a[--a->v.n];                                              \
    if (out) *out = v;                                                   \
    return 0;                                                            \
}                                                                        \
T pt_##SUF##_get(const NAME* a, size_t i){ return a->v.a[i]; }           \
int pt_##SUF##_set(NAME* a, size_t i, T v){                              \
    if (!a || i >= (size_t)a->v.n) return -1;                            \
    a->v.a[i] = v;                                                       \
    return 0;                                                            \
}                                                                        \
void pt_##SUF##_swap(NAME* a, size_t i, size_t j){                       \
    T t = a->v.a[i]; a->v.a[i] = a->v.a[j]; a->v.a[j] = t;               \
}                                                                        \
T* pt_##SUF##_data(NAME* a){ return a ? a->v.a : NULL; }

/* Instantiate all numeric vectors */
#define PT_XNUM(SUF, T, NAME) PT_DEF_VEC_NUMERIC(SUF, T, NAME)
#include "array_types.def"
#undef PT_XNUM

/* ========== String vector (owning char*) — bespoke ========== */

struct PtArrayStr { kvec_t(char*) v; };
PT_DEF_ENSURE_CAP(str, char*)

static void pt_str_free_range(PtArrayStr* a, size_t begin, size_t end){
    /* frees [begin, end) */
    if (!a) return;
    size_t n = (size_t)a->v.n;
    if (begin > end || end > n) return;
    for (size_t i = begin; i < end; ++i) free(a->v.a[i]);
}

PtArrayStr* pt_str_new(void){
    PtArrayStr* a = (PtArrayStr*)calloc(1, sizeof *a);
    if (!a) return NULL;
    kv_init(a->v);
    return a;
}

void pt_str_free(PtArrayStr* a){
    if (!a) return;
    pt_str_free_range(a, 0, (size_t)a->v.n);
    kv_destroy(a->v);
    free(a);
}

size_t pt_str_len(const PtArrayStr* a){ return a ? (size_t)a->v.n : 0; }
size_t pt_str_cap(const PtArrayStr* a){ return a ? (size_t)a->v.m : 0; }

int pt_str_reserve(PtArrayStr* a, size_t cap){
    if (!a) return -1;
    if (cap <= (size_t)a->v.m) return 0;
    size_t n = (size_t)a->v.n;
    return str_ensure_cap(&a->v, cap > n ? (cap - n) : 0);
}

int pt_str_resize(PtArrayStr* a, size_t new_len){
    if (!a) return -1;
    size_t n = (size_t)a->v.n;
    if (new_len <= n) {
        pt_str_free_range(a, new_len, n);
        a->v.n = new_len;
        return 0;
    }
    size_t add = new_len - n;
    if (str_ensure_cap(&a->v, add) != 0) return -1;

    /* allocate first, then commit; rollback cleanly on OOM */
    size_t i = 0;
    for (; i < add; ++i) {
        char* dup = pt_dup_cstr("");
        if (!dup) break;
        a->v.a[n + i] = dup;
    }
    if (i != add) {
        pt_str_free_range(a, n, n + i);
        return -1;
    }
    a->v.n = new_len;
    return 0;
}

int pt_str_push(PtArrayStr* a, const char* s){
    if (!a) return -1;
    if (str_ensure_cap(&a->v, 1) != 0) return -1;
    char* dup = pt_dup_cstr(s);
    if (!dup) return -1;
    a->v.a[a->v.n++] = dup;
    return 0;
}

int pt_str_pop(PtArrayStr* a, char** out){
    if (!a || a->v.n == 0) return -1;
    char* v = a->v.a[--a->v.n];
    if (out) *out = v;   /* caller owns; do not free here */
    return 0;
}

const char* pt_str_get(const PtArrayStr* a, size_t i){ return a->v.a[i]; }

int pt_str_set(PtArrayStr* a, size_t i, const char* s){
    if (!a || i >= (size_t)a->v.n) return -1;
    char* dup = pt_dup_cstr(s);
    if (!dup) return -1;
    free(a->v.a[i]);
    a->v.a[i] = dup;
    return 0;
}

void pt_str_swap(PtArrayStr* a, size_t i, size_t j){
    char* t = a->v.a[i]; a->v.a[i] = a->v.a[j]; a->v.a[j] = t;
}

const char* const* pt_str_data(const PtArrayStr* a){
    return a ? (const char* const*)a->v.a : NULL;
}

/* -------- Stringification helpers (malloc'ed char*) -------- */

typedef struct {
    char* data;
    size_t len;
    size_t cap;
} StrBuf;

static int strbuf_printf(StrBuf* sb, const char* fmt, ...) {
    va_list args, args_copy;
    va_start(args, fmt);
    va_copy(args_copy, args);
    
    size_t avail = sb->cap - sb->len;
    int needed = vsnprintf(sb->data + sb->len, avail, fmt, args);
    if (needed < 0) {
        va_end(args);
        va_end(args_copy);
        return -1;
    }
    
    if ((size_t)needed >= avail) {
        // Grow buffer
        size_t new_cap = sb->cap * 2;
        while (new_cap < sb->len + (size_t)needed + 1) new_cap *= 2;
        char* new_data = realloc(sb->data, new_cap);
        if (!new_data) { va_end(args); va_end(args_copy); return -1; }
        sb->data = new_data;
        sb->cap = new_cap;
        
        // Retry
        int needed2 = vsnprintf(sb->data + sb->len, sb->cap - sb->len, fmt, args_copy);
        if (needed2 < 0) { va_end(args); va_end(args_copy); return -1; }
        needed = needed2;
    }

    sb->len += needed;
    va_end(args);
    va_end(args_copy);
    return 0;
}

const char* array_i64_str(const PtArrayI64* a) {
    StrBuf sb = {malloc(256), 0, 256};
    if (!sb.data) return NULL;
    if (strbuf_printf(&sb, "[") < 0) { free(sb.data); return NULL; }
    for (size_t i = 0; i < pt_i64_len(a); ++i) {
        if (i > 0 && strbuf_printf(&sb, " ") < 0) { free(sb.data); return NULL; }
        if (strbuf_printf(&sb, "%lld", (long long)pt_i64_get(a, i)) < 0) { free(sb.data); return NULL; }
    }
    if (strbuf_printf(&sb, "]") < 0) { free(sb.data); return NULL; }
    return sb.data;
}

const char* array_f64_str(const PtArrayF64* a) {
    StrBuf sb = {malloc(256), 0, 256};
    if (!sb.data) return NULL;
    if (strbuf_printf(&sb, "[") < 0) { free(sb.data); return NULL; }
    for (size_t i = 0; i < pt_f64_len(a); ++i) {
        if (i > 0 && strbuf_printf(&sb, " ") < 0) { free(sb.data); return NULL; }
        if (strbuf_printf(&sb, "%g", (double)pt_f64_get(a, i)) < 0) { free(sb.data); return NULL; }
    }
    if (strbuf_printf(&sb, "]") < 0) { free(sb.data); return NULL; }
    return sb.data;
}

const char* array_str_str(const PtArrayStr* a) {
    StrBuf sb = {malloc(256), 0, 256};
    if (!sb.data) return NULL;
    if (strbuf_printf(&sb, "[") < 0) { free(sb.data); return NULL; }
    size_t n = pt_str_len(a);
    for (size_t i = 0; i < n; ++i) {
        if (i > 0 && strbuf_printf(&sb, " ") < 0) { free(sb.data); return NULL; }
        const char* s = pt_str_get(a, i);
        if (!s) s = "";
        if (strbuf_printf(&sb, "%s", s) < 0) { free(sb.data); return NULL; }
    }
    if (strbuf_printf(&sb, "]") < 0) { free(sb.data); return NULL; }
    return sb.data;
}
