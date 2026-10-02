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

typedef struct {
    int32_t pid;      // the owning app's pid (see audiosources_list_streams)
    char *name;       // malloc'd; the owning app's localized name
    char *bundle_id;  // malloc'd; the owning app's bundle id; may be NULL
} audiosources_stream_t;

// Lists the apps with audio running: input (microphone) streams when
// `input` is nonzero, output (playback) streams otherwise.
// CoreAudio reports these per process, and apps like browsers do their
// audio in helper processes, so each process is resolved to the nearest
// ancestor (or itself) that is a regular app; processes with no such
// app (system daemons) are left out, and so is each app after its first
// entry. Free the result with audiosources_free_streams. Returns the
// count, or -1 if CoreAudio's process list can't be read.
int32_t audiosources_list_streams(int32_t input, audiosources_stream_t **out_streams);

void audiosources_free_streams(audiosources_stream_t *streams, int32_t count);

// Frees an array returned by audiosources_list_active, including each
// entry's strings.
void audiosources_free(audiosources_source_t *sources, int32_t count);

#endif
