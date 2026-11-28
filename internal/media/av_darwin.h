// The AVFoundation side of the macOS media engine: an AVPlayer decodes the
// video into IOSurface-backed BGRA pixel buffers, and the Go engine hands
// their surfaces to the renderer, which wraps them as Metal textures.
#ifndef VIVA_INTERNAL_MEDIA_AV_DARWIN_H
#define VIVA_INTERNAL_MEDIA_AV_DARWIN_H

#include <stddef.h>
#include <stdint.h>

typedef struct viva_media_engine viva_media_engine;

// viva_media_open_url opens a URL AVFoundation accepts, viva_media_open_path
// a file, and viva_media_open_data bytes written to a temporary file that is
// removed when the engine closes. On failure they return NULL and, when
// error is not NULL, an error message the caller frees with
// viva_media_free_error.
viva_media_engine *viva_media_open_url(const char *url, char **error);
viva_media_engine *viva_media_open_path(const char *path, char **error);
viva_media_engine *viva_media_open_data(const void *data, size_t length, char **error);
void viva_media_free_error(char *error);
void viva_media_close(viva_media_engine *engine);

// viva_media_status is the item's status: 0 unknown, 1 ready to play, 2
// failed. viva_media_error describes a failure, malloc'd, or NULL.
int viva_media_status(viva_media_engine *engine);
char *viva_media_error(viva_media_engine *engine);

// viva_media_ended reports whether the item played to its end, until
// viva_media_clear_ended takes the report away. viva_media_waiting reports
// whether the player is playing but waiting for data.
int viva_media_ended(viva_media_engine *engine);
void viva_media_clear_ended(viva_media_engine *engine);
int viva_media_waiting(viva_media_engine *engine);

void viva_media_play(viva_media_engine *engine, double rate);
void viva_media_pause(viva_media_engine *engine);
void viva_media_seek(viva_media_engine *engine, double seconds);
// viva_media_seek_landed reports whether the latest viva_media_seek has
// completed in AVFoundation.
int viva_media_seek_landed(viva_media_engine *engine);
void viva_media_set_rate(viva_media_engine *engine, double rate);
void viva_media_set_volume(viva_media_engine *engine, double volume);
void viva_media_set_muted(viva_media_engine *engine, int muted);
double viva_media_position(viva_media_engine *engine);
double viva_media_duration(viva_media_engine *engine);

// viva_media_has_new_frame reports whether the video output has a picture for
// seconds that viva_media_copy_frame has not returned yet.
int viva_media_has_new_frame(viva_media_engine *engine, double seconds);

// viva_media_copy_frame returns the picture for seconds as a retained
// CVPixelBuffer, or NULL when there is none yet. force takes the picture even
// when the output does not call it new, which is how a paused seek and the
// first picture are read. width and height receive its size, and surface its
// IOSurface, which stays valid until the caller releases the pixel buffer.
void *viva_media_copy_frame(viva_media_engine *engine, double seconds, int force, int *width, int *height, void **surface);
void viva_media_release_frame(void *frame);

#endif