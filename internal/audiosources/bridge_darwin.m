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
#include <sys/sysctl.h>
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

static pid_t parentPID(pid_t pid) {
  struct kinfo_proc info;
  size_t len = sizeof(info);
  int mib[4] = {CTL_KERN, KERN_PROC, KERN_PROC_PID, pid};
  if (sysctl(mib, 4, &info, &len, NULL, 0) != 0 || len == 0) {
    return 0;
  }
  return info.kp_eproc.e_ppid;
}

// The regular app a process belongs to: itself, or the nearest ancestor
// (browser and Electron helpers are children of their app).
static NSRunningApplication *owningApp(pid_t pid, pid_t *appPID) {
  for (int depth = 0; depth < 6 && pid > 1; depth++) {
    NSRunningApplication *app = [NSRunningApplication runningApplicationWithProcessIdentifier:pid];
    if (app != nil && app.activationPolicy == NSApplicationActivationPolicyRegular) {
      *appPID = pid;
      return app;
    }
    pid = parentPID(pid);
  }
  return nil;
}

int32_t audiosources_list_streams(int32_t input, audiosources_stream_t **out_streams) {
  @autoreleasepool {
  AudioObjectPropertyAddress addr = {kAudioHardwarePropertyProcessObjectList,
                                      kAudioObjectPropertyScopeGlobal,
                                      kAudioObjectPropertyElementMain};
  UInt32 size = 0;
  if (AudioObjectGetPropertyDataSize(kAudioObjectSystemObject, &addr, 0, NULL, &size) != noErr) {
    return -1;
  }
  int count = (int)(size / sizeof(AudioObjectID));
  *out_streams = NULL;
  if (count == 0) {
    return 0;
  }
  AudioObjectID *procs = malloc(size);
  if (procs == NULL) {
    return -1;
  }
  if (AudioObjectGetPropertyData(kAudioObjectSystemObject, &addr, 0, NULL, &size, procs) != noErr) {
    free(procs);
    return -1;
  }

  audiosources_stream_t *results = calloc((size_t)count, sizeof(audiosources_stream_t));
  int32_t n = 0;
  AudioObjectPropertySelector running = input ? kAudioProcessPropertyIsRunningInput : kAudioProcessPropertyIsRunningOutput;

  for (int i = 0; i < count; i++) {
    UInt32 isRunning = 0;
    if (getProp(procs[i], running, sizeof(isRunning), &isRunning) != noErr || !isRunning) {
      continue;
    }
    pid_t pid = 0;
    getProp(procs[i], kAudioProcessPropertyPID, sizeof(pid), &pid);
    if (pid <= 0) {
      continue;
    }
    pid_t appPID = 0;
    NSRunningApplication *app = owningApp(pid, &appPID);
    if (app == nil) {
      continue;
    }
    int dup = 0;
    for (int j = 0; j < n; j++) {
      if (results[j].pid == (int32_t)appPID) {
        dup = 1;
        break;
      }
    }
    if (dup) {
      continue;
    }
    results[n].pid = (int32_t)appPID;
    if (app.localizedName != nil) {
      results[n].name = strdup(app.localizedName.UTF8String);
    }
    if (app.bundleIdentifier != nil) {
      results[n].bundle_id = strdup(app.bundleIdentifier.UTF8String);
    }
    n++;
  }
  free(procs);

  if (n == 0) {
    free(results);
    return 0;
  }
  *out_streams = results;
  return n;
  }
}

void audiosources_free_streams(audiosources_stream_t *streams, int32_t count) {
  if (streams == NULL) {
    return;
  }
  for (int32_t i = 0; i < count; i++) {
    free(streams[i].name);
    free(streams[i].bundle_id);
  }
  free(streams);
}
