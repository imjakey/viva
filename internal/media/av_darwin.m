// The AVFoundation side of the macOS media engine. It is written in manual
// retain/release, like the rest of the native backends, and every function
// that makes Objective-C objects drains its own autorelease pool: the Go
// caller has none.
#include "av_darwin.h"

#import <AVFoundation/AVFoundation.h>
#import <CoreMedia/CoreMedia.h>
#import <CoreVideo/CoreVideo.h>
#import <Foundation/Foundation.h>
#import <IOSurface/IOSurface.h>

#include <math.h>
#include <stdatomic.h>
#include <stdlib.h>
#include <string.h>

// VivaMediaEngine plays one item through an AVPlayer and reads the pictures
// the video output decodes into IOSurface-backed pixel buffers.
@interface VivaMediaEngine : NSObject {
@public
    AVPlayer *_player;
    AVPlayerItem *_item;
    AVPlayerItemVideoOutput *_output;
    NSString *_tempPath; // the file memory-held data was written to, or nil
    atomic_bool _ended;
    atomic_uint_least64_t _seekSeq;
    atomic_uint_least64_t _seekDone;
}
- (instancetype)initWithURL:(NSURL *)url error:(NSError **)error;
@end

@implementation VivaMediaEngine

- (instancetype)initWithURL:(NSURL *)url error:(NSError **)error {
    self = [super init];
    if (self == nil) {
        return nil;
    }
    atomic_init(&_ended, false);
    atomic_init(&_seekSeq, 0);
    atomic_init(&_seekDone, 0);
    AVPlayerItem *item = [AVPlayerItem playerItemWithURL:url];
    if (item == nil) {
        if (error != NULL) {
            *error = [NSError errorWithDomain:@"viva.media"
                                         code:1
                                     userInfo:@{NSLocalizedDescriptionKey: @"the video could not be opened"}];
        }
        [self release];
        return nil;
    }
    NSDictionary *attributes = @{
        (NSString *)kCVPixelBufferPixelFormatTypeKey: @(kCVPixelFormatType_32BGRA),
        (NSString *)kCVPixelBufferIOSurfacePropertiesKey: @{},
        (NSString *)kCVPixelBufferMetalCompatibilityKey: @YES,
    };
    AVPlayerItemVideoOutput *output = [[AVPlayerItemVideoOutput alloc] initWithPixelBufferAttributes:attributes];
    if (output == nil) {
        if (error != NULL) {
            *error = [NSError errorWithDomain:@"viva.media"
                                         code:2
                                     userInfo:@{NSLocalizedDescriptionKey: @"the video output could not be made"}];
        }
        [self release];
        return nil;
    }
    [item addOutput:output];
    AVPlayer *player = [[AVPlayer alloc] initWithPlayerItem:item];
    if (player == nil) {
        [output release];
        if (error != NULL) {
            *error = [NSError errorWithDomain:@"viva.media"
                                         code:3
                                     userInfo:@{NSLocalizedDescriptionKey: @"the player could not be made"}];
        }
        [self release];
        return nil;
    }
    // The item's clock stops at its end; the Go engine seeks back and plays
    // again when it loops.
    player.actionAtItemEnd = AVPlayerActionAtItemEndPause;
    _item = [item retain];
    _output = output;
    _player = player;
    [[NSNotificationCenter defaultCenter] addObserver:self
                                             selector:@selector(itemDidPlayToEnd:)
                                                 name:AVPlayerItemDidPlayToEndTimeNotification
                                               object:item];
    return self;
}

- (void)itemDidPlayToEnd:(NSNotification *)notification {
    atomic_store(&_ended, true);
}

- (void)dealloc {
    [[NSNotificationCenter defaultCenter] removeObserver:self];
    [_player pause];
    [_output release];
    [_item release];
    [_player release];
    if (_tempPath != nil) {
        [[NSFileManager defaultManager] removeItemAtPath:_tempPath error:NULL];
        [_tempPath release];
    }
    [super dealloc];
}

@end

// viva_media_engine is the opaque handle Go holds: the Objective-C engine
// and nothing else.
struct viva_media_engine {
    void *object;
};

static void viva_report_failure(char **error, NSError *failure, NSString *fallback) {
    if (error == NULL) {
        return;
    }
    NSString *message = failure != nil ? failure.localizedDescription : fallback;
    if (message == nil || message.UTF8String == NULL) {
        message = @"the video could not be opened";
    }
    *error = strdup(message.UTF8String);
}

static viva_media_engine *viva_media_open(NSURL *url, char **error) {
    char *message = NULL;
    viva_media_engine *engine = NULL;
    @autoreleasepool {
        NSError *failure = nil;
        VivaMediaEngine *object = [[VivaMediaEngine alloc] initWithURL:url error:&failure];
        if (object == nil) {
            viva_report_failure(&message, failure, @"the video could not be opened");
        } else {
            engine = calloc(1, sizeof(*engine));
            if (engine == NULL) {
                [object release];
                viva_report_failure(&message, nil, @"memory for the video engine ran out");
            } else {
                engine->object = object;
            }
        }
    }
    if (error != NULL) {
        *error = message;
    } else if (message != NULL) {
        free(message);
    }
    return engine;
}

viva_media_engine *viva_media_open_url(const char *url, char **error) {
    @autoreleasepool {
        NSString *string = url != NULL ? [NSString stringWithUTF8String:url] : nil;
        NSURL *location = string != nil ? [NSURL URLWithString:string] : nil;
        if (location == nil) {
            viva_report_failure(error, nil, @"the video's URL is not valid");
            return NULL;
        }
        return viva_media_open(location, error);
    }
}

viva_media_engine *viva_media_open_path(const char *path, char **error) {
    @autoreleasepool {
        if (path == NULL) {
            viva_report_failure(error, nil, @"the video's path is empty");
            return NULL;
        }
        NSString *string = [NSString stringWithUTF8String:path];
        if (string == nil) {
            viva_report_failure(error, nil, @"the video's path is not valid");
            return NULL;
        }
        return viva_media_open([NSURL fileURLWithPath:string], error);
    }
}

viva_media_engine *viva_media_open_data(const void *data, size_t length, char **error) {
    @autoreleasepool {
        if (data == NULL || length == 0) {
            viva_report_failure(error, nil, @"the video's data is empty");
            return NULL;
        }
        // AVFoundation plays files, so data goes to a temporary file beside
        // the others the system keeps, and Close takes it away again. Pick
        // the container extension from magic bytes so MOV/M4V/WebM are not
        // misidentified as MP4.
        NSString *ext = @"mp4";
        const uint8_t *raw = (const uint8_t *)data;
        if (length >= 12 && memcmp(raw + 4, "ftyp", 4) == 0) {
            if (memcmp(raw + 8, "qt  ", 4) == 0) {
                ext = @"mov";
            } else if (memcmp(raw + 8, "M4V ", 4) == 0 || memcmp(raw + 8, "m4v ", 4) == 0) {
                ext = @"m4v";
            }
        } else if (length >= 4 && memcmp(raw, "\x1a\x45\xdf\xa3", 4) == 0) {
            ext = @"webm";
        }
        NSString *name = [[[NSUUID UUID] UUIDString] stringByAppendingPathExtension:ext];
        NSString *path = [NSTemporaryDirectory() stringByAppendingPathComponent:name];
        NSData *bytes = [NSData dataWithBytesNoCopy:(void *)data length:length freeWhenDone:NO];
        NSError *failure = nil;
        if (![bytes writeToFile:path options:NSDataWritingAtomic error:&failure]) {
            viva_report_failure(error, failure, @"the video's data could not be written");
            return NULL;
        }
        viva_media_engine *engine = viva_media_open([NSURL fileURLWithPath:path], error);
        if (engine == NULL) {
            [[NSFileManager defaultManager] removeItemAtPath:path error:NULL];
            return NULL;
        }
        ((VivaMediaEngine *)engine->object)->_tempPath = [path copy];
        return engine;
    }
}

void viva_media_free_error(char *error) {
    free(error);
}

void viva_media_close(viva_media_engine *engine) {
    if (engine == NULL) {
        return;
    }
    @autoreleasepool {
        [(VivaMediaEngine *)engine->object release];
        free(engine);
    }
}

int viva_media_status(viva_media_engine *engine) {
    if (engine == NULL || engine->object == NULL) {
        return 0;
    }
    @autoreleasepool {
        AVPlayerItemStatus status = ((VivaMediaEngine *)engine->object)->_item.status;
        switch (status) {
            case AVPlayerItemStatusReadyToPlay:
                return 1;
            case AVPlayerItemStatusFailed:
                return 2;
            default:
                return 0;
        }
    }
}

char *viva_media_error(viva_media_engine *engine) {
    if (engine == NULL || engine->object == NULL) {
        return NULL;
    }
    @autoreleasepool {
        NSError *failure = ((VivaMediaEngine *)engine->object)->_item.error;
        if (failure == nil) {
            return NULL;
        }
        const char *message = failure.localizedDescription.UTF8String;
        if (message == NULL) {
            message = "the video failed to play";
        }
        return strdup(message);
    }
}

int viva_media_ended(viva_media_engine *engine) {
    if (engine == NULL || engine->object == NULL) {
        return 0;
    }
    return atomic_load(&((VivaMediaEngine *)engine->object)->_ended) ? 1 : 0;
}

void viva_media_clear_ended(viva_media_engine *engine) {
    if (engine == NULL || engine->object == NULL) {
        return;
    }
    atomic_store(&((VivaMediaEngine *)engine->object)->_ended, false);
}

int viva_media_waiting(viva_media_engine *engine) {
    if (engine == NULL || engine->object == NULL) {
        return 0;
    }
    @autoreleasepool {
        return ((VivaMediaEngine *)engine->object)->_player.timeControlStatus == AVPlayerTimeControlStatusWaitingToPlayAtSpecifiedRate ? 1 : 0;
    }
}

void viva_media_play(viva_media_engine *engine, double rate) {
    if (engine == NULL || engine->object == NULL) {
        return;
    }
    @autoreleasepool {
        AVPlayer *player = ((VivaMediaEngine *)engine->object)->_player;
        [player play];
        player.rate = (float)rate;
    }
}

void viva_media_pause(viva_media_engine *engine) {
    if (engine == NULL || engine->object == NULL) {
        return;
    }
    @autoreleasepool {
        ((VivaMediaEngine *)engine->object)->_player.rate = 0;
    }
}

void viva_media_seek(viva_media_engine *engine, double seconds) {
    if (engine == NULL || engine->object == NULL) {
        return;
    }
    @autoreleasepool {
        VivaMediaEngine *me = (VivaMediaEngine *)engine->object;
        uint64_t seq = atomic_fetch_add(&me->_seekSeq, 1) + 1;
        CMTime time = CMTimeMakeWithSeconds(seconds, 600);
        [me->_player seekToTime:time
                toleranceBefore:kCMTimeZero
                 toleranceAfter:kCMTimeZero
              completionHandler:^(BOOL finished) {
                  // Only mark landed when this seek completed or is still the
                  // latest seek (superseding seeks cancel earlier ones).
                  if (finished || atomic_load(&me->_seekSeq) == seq) {
                      uint64_t prev = atomic_load(&me->_seekDone);
                      while (prev < seq && !atomic_compare_exchange_weak(&me->_seekDone, &prev, seq)) {
                      }
                  }
              }];
    }
}

int viva_media_seek_landed(viva_media_engine *engine) {
    if (engine == NULL || engine->object == NULL) {
        return 1;
    }
    VivaMediaEngine *me = (VivaMediaEngine *)engine->object;
    return atomic_load(&me->_seekDone) >= atomic_load(&me->_seekSeq) ? 1 : 0;
}

void viva_media_set_rate(viva_media_engine *engine, double rate) {
    if (engine == NULL || engine->object == NULL) {
        return;
    }
    @autoreleasepool {
        ((VivaMediaEngine *)engine->object)->_player.rate = (float)rate;
    }
}

void viva_media_set_volume(viva_media_engine *engine, double volume) {
    if (engine == NULL || engine->object == NULL) {
        return;
    }
    @autoreleasepool {
        ((VivaMediaEngine *)engine->object)->_player.volume = (float)volume;
    }
}

void viva_media_set_muted(viva_media_engine *engine, int muted) {
    if (engine == NULL || engine->object == NULL) {
        return;
    }
    @autoreleasepool {
        ((VivaMediaEngine *)engine->object)->_player.muted = muted != 0;
    }
}

double viva_media_position(viva_media_engine *engine) {
    if (engine == NULL || engine->object == NULL) {
        return 0;
    }
    @autoreleasepool {
        double seconds = CMTimeGetSeconds(((VivaMediaEngine *)engine->object)->_player.currentTime);
        return isfinite(seconds) && seconds > 0 ? seconds : 0;
    }
}

double viva_media_duration(viva_media_engine *engine) {
    if (engine == NULL || engine->object == NULL) {
        return 0;
    }
    @autoreleasepool {
        double seconds = CMTimeGetSeconds(((VivaMediaEngine *)engine->object)->_item.duration);
        return isfinite(seconds) && seconds > 0 ? seconds : 0;
    }
}

int viva_media_has_new_frame(viva_media_engine *engine, double seconds) {
    if (engine == NULL || engine->object == NULL) {
        return 0;
    }
    @autoreleasepool {
        CMTime time = CMTimeMakeWithSeconds(seconds, 600);
        return [((VivaMediaEngine *)engine->object)->_output hasNewPixelBufferForItemTime:time] ? 1 : 0;
    }
}

void *viva_media_copy_frame(viva_media_engine *engine, double seconds, int force, int *width, int *height, void **surface) {
    if (engine == NULL || engine->object == NULL) {
        return NULL;
    }
    @autoreleasepool {
        AVPlayerItemVideoOutput *output = ((VivaMediaEngine *)engine->object)->_output;
        CMTime time = CMTimeMakeWithSeconds(seconds, 600);
        if (force == 0 && ![output hasNewPixelBufferForItemTime:time]) {
            return NULL;
        }
        CVPixelBufferRef buffer = [output copyPixelBufferForItemTime:time itemTimeForDisplay:NULL];
        if (buffer == NULL) {
            return NULL;
        }
        IOSurfaceRef shape = CVPixelBufferGetIOSurface(buffer);
        if (shape == NULL) {
            CVPixelBufferRelease(buffer);
            return NULL;
        }
        if (width != NULL) {
            *width = (int)CVPixelBufferGetWidth(buffer);
        }
        if (height != NULL) {
            *height = (int)CVPixelBufferGetHeight(buffer);
        }
        if (surface != NULL) {
            *surface = (void *)shape;
        }
        return buffer;
    }
}

void viva_media_release_frame(void *frame) {
    if (frame != NULL) {
        CVPixelBufferRelease((CVPixelBufferRef)frame);
    }
}