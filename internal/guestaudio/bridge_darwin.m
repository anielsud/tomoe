// Objective-C shim for ScreenCaptureKit guest-audio capture. Kept
// deliberately thin: this file only ever makes the real SCK calls and
// copies raw bytes across the boundary; the actual downmix/interpretation
// logic lives in Go (callback_darwin.go), where it's easier to read, test,
// and keep honest about what the data actually is.
//
// Ported from tomoe-darwin's validated Python/pyobjc spike
// (guest_audio_tap/sck_capture.py) -- same mechanism, same two real
// gotchas already found and fixed there, carried forward here instead of
// re-discovered:
//   1. SCStream needs a window-server connection a bare process doesn't
//      have by default -- guestaudio_ensure_app_context() below.
//   2. CMBlockBufferCopyDataBytes's C signature returns its status by
//      value and writes through an out-pointer, both handled directly and
//      correctly here -- the bug in Python was pyobjc's own
//      out-parameter-to-tuple bridging convention silently discarding
//      every real buffer, which has no equivalent failure mode when
//      calling the real C ABI directly, as cgo does.
#import <AppKit/AppKit.h>
#import <CoreMedia/CoreMedia.h>
#import <ScreenCaptureKit/ScreenCaptureKit.h>
#include <stdlib.h>
#include <string.h>

#include "bridge.h"
#include "_cgo_export.h"

@interface GuestAudioOutput : NSObject <SCStreamOutput>
@property(nonatomic, assign) uintptr_t goHandle;
@end

@implementation GuestAudioOutput
- (void)stream:(SCStream *)stream
    didOutputSampleBuffer:(CMSampleBufferRef)sampleBuffer
                    ofType:(SCStreamOutputType)type {
  if (type != SCStreamOutputTypeAudio) {
    return;
  }
  CMBlockBufferRef blockBuffer = CMSampleBufferGetDataBuffer(sampleBuffer);
  if (blockBuffer == NULL) {
    return;
  }
  size_t length = CMBlockBufferGetDataLength(blockBuffer);
  if (length < sizeof(float)) {
    return;
  }
  void *raw = malloc(length);
  if (raw == NULL) {
    return;
  }
  // Real C ABI: status returned by value, bytes written through the
  // out-pointer we pass in. No out-parameter reinterpretation possible.
  OSStatus err = CMBlockBufferCopyDataBytes(blockBuffer, 0, length, raw);
  if (err != kCMBlockBufferNoErr) {
    free(raw);
    return;
  }

  double sampleRate = 48000.0;
  CMFormatDescriptionRef fmt = CMSampleBufferGetFormatDescription(sampleBuffer);
  if (fmt != NULL) {
    const AudioStreamBasicDescription *asbd =
        CMAudioFormatDescriptionGetStreamBasicDescription(fmt);
    if (asbd != NULL && asbd->mSampleRate > 0) {
      sampleRate = asbd->mSampleRate;
    }
  }

  goGuestAudioOnSamples(self.goHandle, (float *)raw, (int)(length / sizeof(float)), sampleRate);
  free(raw);
}
@end

static void guestaudio_ensure_app_context(void) {
  static dispatch_once_t once;
  dispatch_once(&once, ^{
    [NSApplication sharedApplication];
  });
}

void *guestaudio_start_tap(int32_t window_id, uintptr_t go_handle, char **out_error) {
  guestaudio_ensure_app_context();

  __block SCWindow *targetWindow = nil;
  dispatch_semaphore_t findSem = dispatch_semaphore_create(0);
  [SCShareableContent getShareableContentWithCompletionHandler:^(SCShareableContent *content,
                                                                  NSError *error) {
    if (content != nil) {
      for (SCWindow *w in content.windows) {
        if (w.windowID == (CGWindowID)window_id) {
          targetWindow = w;
          break;
        }
      }
    }
    dispatch_semaphore_signal(findSem);
  }];
  dispatch_semaphore_wait(findSem, DISPATCH_TIME_FOREVER);

  if (targetWindow == nil) {
    *out_error = strdup("window not found in SCShareableContent");
    return NULL;
  }

  SCContentFilter *filter = [[SCContentFilter alloc] initWithDesktopIndependentWindow:targetWindow];
  SCStreamConfiguration *config = [[SCStreamConfiguration alloc] init];
  config.capturesAudio = YES;
  config.excludesCurrentProcessAudio = YES;

  SCStream *stream = [[SCStream alloc] initWithFilter:filter configuration:config delegate:nil];

  GuestAudioOutput *output = [[GuestAudioOutput alloc] init];
  output.goHandle = go_handle;

  NSError *addErr = nil;
  BOOL ok = [stream addStreamOutput:output
                                type:SCStreamOutputTypeAudio
                  sampleHandlerQueue:NULL
                               error:&addErr];
  if (!ok) {
    *out_error = strdup(addErr.localizedDescription.UTF8String);
    return NULL;
  }

  __block NSError *startErr = nil;
  dispatch_semaphore_t startSem = dispatch_semaphore_create(0);
  [stream startCaptureWithCompletionHandler:^(NSError *error) {
    startErr = error;
    dispatch_semaphore_signal(startSem);
  }];
  dispatch_semaphore_wait(startSem, DISPATCH_TIME_FOREVER);

  if (startErr != nil) {
    *out_error = strdup(startErr.localizedDescription.UTF8String);
    return NULL;
  }

  // `stream` retains `output` for as long as it's a registered stream
  // output (SCStream's own documented behavior) -- CFBridgingRetain here
  // hands the *stream* reference to Go/C as a manually-managed pointer;
  // Go must call guestaudio_stop_tap exactly once to balance it.
  return (void *)CFBridgingRetain(stream);
}

void guestaudio_stop_tap(void *tap) {
  if (tap == NULL) {
    return;
  }
  // Transfers the reference back to ARC, balancing the CFBridgingRetain
  // above; `stream` is released automatically when this function returns.
  SCStream *stream = (SCStream *)CFBridgingRelease(tap);
  dispatch_semaphore_t sem = dispatch_semaphore_create(0);
  [stream stopCaptureWithCompletionHandler:^(NSError *error) {
    dispatch_semaphore_signal(sem);
  }];
  dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);
}
