#ifndef VIDEOHINT_OCR_BRIDGE_H
#define VIDEOHINT_OCR_BRIDGE_H

#include <stdint.h>

// Runs Vision.framework text recognition against a packed RGB (no
// alpha, no row padding) pixel buffer -- the same shape
// teamsvideo.Frame.Pix and DetectRing's pix parameter already use.
// Returns a malloc'd, newline-joined string of all recognized text
// lines (caller must free), or NULL with *out_error set to a malloc'd
// error string (caller must free) on failure.
char *videohint_recognize_text(const uint8_t *rgb, int32_t width, int32_t height, char **out_error);

#endif
