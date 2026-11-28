//go:build darwin

package platform

// NativeTextures reports whether the platform's renderer draws GPU textures
// shared by a media engine. The macOS renderer wraps the engine's IOSurfaces
// as Metal textures, as the Windows renderer opens its NT handles.
const NativeTextures = true
