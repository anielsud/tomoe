// CoreAudio process-object enumeration -- confirmed live (see
// audiosources_darwin.go's doc comment) that querying
// kAudioProcessPropertyIsRunningOutput correctly reflects real-time
// audio activity, including for a process that had no audio activity
// moments before. Kept as thin, boring property-getter code, matching
// this project's other cgo/ObjC bridges' own stated style.
#import <CoreAudio/CoreAudio.h>
#import <AudioToolbox/AudioToolbox.h>
#import <AppKit/AppKit.h>
#include <stdlib.h>
#include <string.h>

#include "bridge.h"

static OSStatus getProp(AudioObjectID obj, AudioObjectPropertySelector sel, UInt32 size, void *data) {
  AudioObjectPropertyAddress addr = {sel, kAudioObjectPropertyScopeGlobal, kAudioObjectPropertyElementMain};
  return AudioObjectGetPropertyData(obj, &addr, 0, NULL, &size, data);
}

int32_t audiosources_list_active(audiosources_source_t **out_sources) {
  // Polled repeatedly (the frontend's source picker refreshes this
  // periodically) -- wrap in a pool so each call's autoreleased objects
  // (NSRunningApplication lookups below) get cleaned up promptly rather
  // than accumulating in whatever pool this cgo call happens to run
  // under.
  @autoreleasepool {
  AudioObjectPropertyAddress addr = {kAudioHardwarePropertyProcessObjectList,
                                      kAudioObjectPropertyScopeGlobal,
                                      kAudioObjectPropertyElementMain};
  UInt32 size = 0;
  OSStatus err = AudioObjectGetPropertyDataSize(kAudioObjectSystemObject, &addr, 0, NULL, &size);
  if (err != noErr) {
    return -1;
  }
  int count = (int)(size / sizeof(AudioObjectID));
  if (count == 0) {
    return 0;
  }

  AudioObjectID *procs = malloc(size);
  if (procs == NULL) {
    return -1;
  }
  err = AudioObjectGetPropertyData(kAudioObjectSystemObject, &addr, 0, NULL, &size, procs);
  if (err != noErr) {
    free(procs);
    return -1;
  }

  audiosources_source_t *results = calloc((size_t)count, sizeof(audiosources_source_t));
  int32_t n = 0;

  for (int i = 0; i < count; i++) {
    AudioObjectID procObj = procs[i];

    UInt32 isRunningOutput = 0;
    if (getProp(procObj, kAudioProcessPropertyIsRunningOutput, sizeof(isRunningOutput), &isRunningOutput) != noErr) {
      continue;
    }
    if (!isRunningOutput) {
      continue;
    }

    pid_t pid = 0;
    getProp(procObj, kAudioProcessPropertyPID, sizeof(pid), &pid);
    if (pid <= 0) {
      continue;
    }

    results[n].pid = (int32_t)pid;

    CFStringRef bundleID = NULL;
    if (getProp(procObj, kAudioProcessPropertyBundleID, sizeof(bundleID), &bundleID) == noErr && bundleID != NULL) {
      // kAudioProcessPropertyBundleID hands back a CFStringRef the
      // caller owns (Apple's "Get" property convention for CF types) --
      // this file builds without ARC (matching this project's other
      // ObjC bridges), so that release has to be explicit.
      CFIndex len = CFStringGetMaximumSizeForEncoding(CFStringGetLength(bundleID), kCFStringEncodingUTF8) + 1;
      char *buf = malloc((size_t)len);
      if (buf != NULL && CFStringGetCString(bundleID, buf, len, kCFStringEncodingUTF8)) {
        results[n].bundle_id = buf;
      } else {
        free(buf);
      }
      CFRelease(bundleID);
    }

    NSRunningApplication *app = [NSRunningApplication runningApplicationWithProcessIdentifier:pid];
    if (app != nil && app.localizedName != nil) {
      results[n].name = strdup(app.localizedName.UTF8String);
    }

    n++;
  }
  free(procs);

  if (n == 0) {
    free(results);
    *out_sources = NULL;
    return 0;
  }

  *out_sources = results;
  return n;
  }
}

void audiosources_free(audiosources_source_t *sources, int32_t count) {
  if (sources == NULL) {
    return;
  }
  for (int32_t i = 0; i < count; i++) {
    free(sources[i].name);
    free(sources[i].bundle_id);
  }
  free(sources);
}
