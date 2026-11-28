# Video

`ui/video` plays video as pictures. The application declares a player in its
model, opens it on a file, a URL or bytes, and shows a `View` where the
picture goes. The application gives the player its own controls: there is no
built-in control bar, and nothing plays until `Play` is called.

```go
//go:embed trailer.mp4
var trailer []byte

m.player, err = video.OpenData(trailer)
if err != nil {
    // ErrUnsupported on a system with no media framework: show a GIF instead.
}
m.player.Fit(video.Cover).SetLoop(false)
...
video.View(c, "Trailer", m.player).Fill().Height(240)
if ui.Button(c, "Pause").Clicked() {
    m.player.Pause()
}
```

A player opens paused and loads in the background: `Size` is 0×0 and `State`
is `Loading` until its first picture, which wakes the page through the
window's invalidator. `Play` starts it; `Pause` stops it where it is. `Seek`
starts it at the nearest frame at or before the time, `Position` says where it
is, and `Duration` how long it is, 0 while unknown. `State` reports `Ended`
when it played to its end, and `Err` what stopped it. `SetLoop` makes it play
again by itself, `SetRate` sets how fast, and `SetVolume` and `SetMuted` its
sound.

`Fit` chooses how the picture fills the view's box, and returns the player for
chaining: `Contain` (the default) fits the whole picture inside and leaves the
rest empty, `Cover` fills the box and clips the overflow, and `Fill` stretches
the picture to the box.

## Sources

- `Open(path)` opens a file.
- `OpenURL(url)` opens a `file:`, `http:` or `https:` URL, as the system's
  media framework accepts it.
- `OpenData(data)` plays bytes in memory. Windows copies them into a memory
  stream (`SHCreateMemStream`); on macOS AVFoundation reads a file, so the
  engine writes them to a temporary file, which it removes when the player
  closes. The caller may reuse or modify `data` after `OpenData` returns.

The system's media framework decodes: Media Foundation on Windows, where
H.264 in MP4 and MOV play with their sound, in hardware where the machine
has it; AVFoundation on macOS, which plays what the system plays. Their
pictures reach the window's renderer where the decoder left them, on the GPU.
On Windows the engine shares an NT handle of a Direct3D texture, and the
renderer opens it on its own device, the two synchronizing with a keyed
mutex. On macOS the engine hands over a `CVPixelBuffer`'s IOSurface, and the
renderer wraps it as a Metal texture. Either way a frame is drawn without a
copy through memory. A build for another system, a Windows without Media
Foundation, or a macOS built without cgo, returns `ErrUnsupported` from all
three Open functions.

## GIF and APNG

`NewGIFDecoder(data)` and `NewAPNGDecoder(data)` decode animated images in Go,
so they play on every system. Their frames are composed as they play, as
browsers show them: a frame whose delay is 10 ms or less shows for 100 ms, a
GIF's disposal and an APNG's dispose and blend are honored, and a GIF that ends
early plays the frames it has whole. Pass one to `NewDecoder`:

```go
d, err := video.NewGIFDecoder(wavesGIF)
...
waves := video.NewDecoder(d).Fit(video.Cover)
waves.SetLoop(true)
waves.Play()
```

A decoder's frames have no sound, so `SetVolume` and `SetMuted` do nothing.
Both decoders know how many frames come before the one showing at a time, so
`Seek` starts there and composes the frames before it; a decoder that cannot
seek ignores `Seek`, and `Position` stays where it was. A GIF decodes into
memory at once, a byte a pixel for each frame; an APNG keeps each frame
compressed and decodes it as it plays.

## Shapes

`Decoder` is the contract behind `NewDecoder`, so an application may bring its
own picture source: `Next` returns each frame, premultiplied BGRA, and
`Rewind` starts over. `Seeker`, a `Decoder` that also has `Seek`, lets the
player jump without replaying; a `Duration` method of its own tells the
player's `Duration` before the end is reached. The player decodes a couple of
frames ahead on its own goroutine and shows each frame when the canvas clock
passes its time, so the page paints once per frame of the clip and not at all
between frames.

## Lifetime and threading

A player's methods are called on the UI thread. Keep one in the model, declare
its view in the page's own paint (not inside a `Memo`, or the view may not be
there when the picture wakes the page), and do not copy the player after it
has been shown. `Close` stops the video, frees the decoder or engine, and
closes the textures the engine shared; it is safe to call more than once. A
view of a closed player paints nothing.

## What it is not

- It shows pictures, not controls, subtitles or a playlist.
- It plays what the system plays. A codec the machine's media framework does
  not have is an error the page can show with `Err`, not something Viva
  decodes itself.
- It is not a camera or a capture API.