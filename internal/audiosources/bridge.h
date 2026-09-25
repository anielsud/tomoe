#ifndef AUDIOSOURCES_BRIDGE_H
#define AUDIOSOURCES_BRIDGE_H

#include <stdint.h>

typedef struct {
    int32_t pid;
    char *name;      // malloc'd, caller must free (via audiosources_free)
    char *bundle_id;  // malloc'd, caller must free (via audiosources_free); may be NULL
} audiosources_source_t;

// Queries every CoreAudio process object and returns the ones currently
// producing audio output (kAudioProcessPropertyIsRunningOutput) via
// *out_sources (malloc'd array, caller must free with audiosources_free).
// Returns the count, or -1 on failure to even enumerate process objects
// (a process failing to resolve its own name/bundle is not a failure of
// the whole call -- that entry just gets empty fields).
int32_t audiosources_list_active(audiosources_source_t **out_sources);

// Frees an array returned by audiosources_list_active, including each
// entry's strings.
void audiosources_free(audiosources_source_t *sources, int32_t count);

#endif
