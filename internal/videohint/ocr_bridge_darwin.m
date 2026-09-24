// Objective-C shim for Vision.framework text recognition. Kept
// deliberately thin, matching internal/guestaudio's bridge_darwin.m
// pattern: this file only ever makes the real Vision calls and copies
// a plain string across the boundary; nothing else lives here.
#import <Vision/Vision.h>
#import <CoreGraphics/CoreGraphics.h>
#import <Foundation/Foundation.h>
#include <stdlib.h>
#include <string.h>

#include "ocr_bridge.h"

char *videohint_recognize_text(const uint8_t *rgb, int32_t width, int32_t height, char **out_error) {
  @autoreleasepool {
  // NSData's dataWithBytes: copies immediately, so CGDataProviderCreateWithCFData
  // never holds a reference to the caller's (Go-owned) buffer beyond this call --
  // unlike CGDataProviderCreateWithData, which wraps the pointer directly and is
  // not safe to hand a Go slice's backing array across the cgo boundary.
  NSData *copy = [NSData dataWithBytes:rgb length:(NSUInteger)(width * height * 3)];
  CGColorSpaceRef colorSpace = CGColorSpaceCreateDeviceRGB();
  CGDataProviderRef provider = CGDataProviderCreateWithCFData((CFDataRef)copy);
  CGImageRef image = CGImageCreate(
      (size_t)width, (size_t)height,
      8, 24, (size_t)(width * 3),
      colorSpace,
      kCGBitmapByteOrderDefault | kCGImageAlphaNone,
      provider, NULL, false, kCGRenderingIntentDefault);
  CGDataProviderRelease(provider);
  CGColorSpaceRelease(colorSpace);

  if (image == NULL) {
    *out_error = strdup("failed to create CGImage from pixel buffer");
    return NULL;
  }

  VNImageRequestHandler *handler = [[VNImageRequestHandler alloc] initWithCGImage:image options:@{}];
  CGImageRelease(image);

  // This file builds without ARC (matching the rest of this project's
  // cgo/ObjC bridges), so anything assigned into these __block variables
  // from inside the completion handler below must be explicitly retained --
  // Vision hands back autoreleased objects, and without a retain here they
  // get freed out from under us the moment the handler's own autorelease
  // pool (or ours) drains, before we read them back outside the block.
  __block NSString *resultText = nil;
  __block NSError *visionError = nil;
  // VNImageRequestHandler's performRequests:error: is synchronous (unlike
  // VNSequenceRequestHandler, used for video) -- the completion handler
  // below has already run by the time it returns. The semaphore is just
  // defensive belt-and-suspenders, not load-bearing.
  dispatch_semaphore_t sem = dispatch_semaphore_create(0);

  VNRecognizeTextRequest *request = [[VNRecognizeTextRequest alloc]
      initWithCompletionHandler:^(VNRequest *req, NSError *error) {
        if (error != nil) {
          visionError = [error retain];
          dispatch_semaphore_signal(sem);
          return;
        }
        NSMutableArray<NSString *> *lines = [NSMutableArray array];
        for (VNRecognizedTextObservation *obs in req.results) {
          VNRecognizedText *top = [[obs topCandidates:1] firstObject];
          if (top != nil) {
            [lines addObject:top.string];
          }
        }
        resultText = [[lines componentsJoinedByString:@"\n"] retain];
        dispatch_semaphore_signal(sem);
      }];
  request.recognitionLevel = VNRequestTextRecognitionLevelAccurate;

  NSError *performError = nil;
  BOOL ok = [handler performRequests:@[ request ] error:&performError];
  [request release];
  [handler release];
  if (!ok) {
    *out_error = strdup(performError.localizedDescription.UTF8String);
    return NULL;
  }

  dispatch_semaphore_wait(sem, DISPATCH_TIME_FOREVER);

  if (visionError != nil) {
    *out_error = strdup(visionError.localizedDescription.UTF8String);
    [visionError release];
    return NULL;
  }

  const char *utf8 = resultText != nil ? resultText.UTF8String : "";
  char *out = strdup(utf8);
  [resultText release];
  return out;
  }
}
