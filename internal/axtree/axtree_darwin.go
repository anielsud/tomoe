//go:build darwin

package axtree

/*
#cgo LDFLAGS: -framework ApplicationServices -framework CoreFoundation
#include <ApplicationServices/ApplicationServices.h>
#include <stdlib.h>
#include <string.h>

typedef struct {
	char *role, *desc, *title;
	double x, y, w, h;
} ax_elem_t;

typedef struct {
	ax_elem_t *items;
	int n, cap, max;
} ax_list_t;

static char *ax_string(AXUIElementRef e, CFStringRef attr) {
	CFTypeRef v = NULL;
	if (AXUIElementCopyAttributeValue(e, attr, &v) != kAXErrorSuccess || v == NULL) return NULL;
	char *out = NULL;
	if (CFGetTypeID(v) == CFStringGetTypeID()) {
		CFIndex len = CFStringGetMaximumSizeForEncoding(CFStringGetLength((CFStringRef)v), kCFStringEncodingUTF8) + 1;
		out = malloc(len);
		if (!CFStringGetCString((CFStringRef)v, out, len, kCFStringEncodingUTF8)) { free(out); out = NULL; }
	}
	CFRelease(v);
	return out;
}

static void ax_rect(AXUIElementRef e, double *x, double *y, double *w, double *h) {
	CFTypeRef v = NULL;
	CGPoint p = {0, 0};
	CGSize s = {0, 0};
	if (AXUIElementCopyAttributeValue(e, kAXPositionAttribute, &v) == kAXErrorSuccess && v) { AXValueGetValue((AXValueRef)v, kAXValueTypeCGPoint, &p); CFRelease(v); }
	v = NULL;
	if (AXUIElementCopyAttributeValue(e, kAXSizeAttribute, &v) == kAXErrorSuccess && v) { AXValueGetValue((AXValueRef)v, kAXValueTypeCGSize, &s); CFRelease(v); }
	*x = p.x; *y = p.y; *w = s.width; *h = s.height;
}

static void ax_walk(AXUIElementRef e, int depth, ax_list_t *l) {
	if (depth > 40 || l->n >= l->max) return;
	char *role = ax_string(e, kAXRoleAttribute);
	char *desc = ax_string(e, kAXDescriptionAttribute);
	char *title = NULL;
	int isWindow = role && strcmp(role, "AXWindow") == 0;
	if (isWindow) title = ax_string(e, kAXTitleAttribute);
	if ((desc && desc[0]) || isWindow) {
		if (l->n == l->cap) { l->cap = l->cap ? l->cap * 2 : 64; l->items = realloc(l->items, l->cap * sizeof(ax_elem_t)); }
		ax_elem_t *it = &l->items[l->n++];
		it->role = role; it->desc = desc; it->title = title;
		ax_rect(e, &it->x, &it->y, &it->w, &it->h);
	} else {
		free(role); free(desc); free(title);
	}
	CFTypeRef kids = NULL;
	if (AXUIElementCopyAttributeValue(e, kAXChildrenAttribute, &kids) == kAXErrorSuccess && kids) {
		if (CFGetTypeID(kids) == CFArrayGetTypeID()) {
			CFIndex n = CFArrayGetCount((CFArrayRef)kids);
			for (CFIndex i = 0; i < n; i++) ax_walk((AXUIElementRef)CFArrayGetValueAtIndex((CFArrayRef)kids, i), depth + 1, l);
		}
		CFRelease(kids);
	}
}

// ax_collect walks every window of pid. Returns -1 when the app may not
// be read (Accessibility permission not granted).
static int ax_collect(int pid, int max, ax_list_t *l) {
	if (!AXIsProcessTrusted()) return -1;
	AXUIElementRef app = AXUIElementCreateApplication((pid_t)pid);
	AXUIElementSetMessagingTimeout(app, 1.0);
	l->max = max;
	CFTypeRef wins = NULL;
	if (AXUIElementCopyAttributeValue(app, kAXWindowsAttribute, &wins) == kAXErrorSuccess && wins) {
		if (CFGetTypeID(wins) == CFArrayGetTypeID()) {
			CFIndex n = CFArrayGetCount((CFArrayRef)wins);
			for (CFIndex i = 0; i < n; i++) ax_walk((AXUIElementRef)CFArrayGetValueAtIndex((CFArrayRef)wins, i), 0, l);
		}
		CFRelease(wins);
	}
	CFRelease(app);
	return l->n;
}

static ax_elem_t *ax_at(ax_list_t *l, int i) { return &l->items[i]; }

static void ax_free(ax_list_t *l) {
	for (int i = 0; i < l->n; i++) { free(l->items[i].role); free(l->items[i].desc); free(l->items[i].title); }
	free(l->items);
}
*/
import "C"

import "errors"

// ErrNotTrusted means the app hasn't been granted Accessibility
// permission, so other apps' trees can't be read.
var ErrNotTrusted = errors.New("axtree: Accessibility permission not granted")

// maxElements bounds one read (a large gallery has a few hundred).
const maxElements = 4000

// Read returns the described elements and the windows of app pid, in tree
// order. Each call asks the app afresh (tens of milliseconds).
func Read(pid int) ([]Element, error) {
	var l C.ax_list_t
	n := int(C.ax_collect(C.int(pid), C.int(maxElements), &l))
	defer C.ax_free(&l)
	if n < 0 {
		return nil, ErrNotTrusted
	}
	out := make([]Element, 0, n)
	for i := 0; i < n; i++ {
		it := C.ax_at(&l, C.int(i))
		e := Element{Rect: Rect{float64(it.x), float64(it.y), float64(it.w), float64(it.h)}}
		if it.role != nil {
			e.Role = C.GoString(it.role)
		}
		if it.desc != nil {
			e.Description = C.GoString(it.desc)
		}
		if it.title != nil {
			e.Title = C.GoString(it.title)
		}
		out = append(out, e)
	}
	return out, nil
}
