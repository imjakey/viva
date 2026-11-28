//go:build !windows && !darwin

package platform

// NativeTextures reports whether the platform's renderer draws GPU textures
// shared by a media engine. Windows and macOS do in this release; the other
// backends drop a texture image, whose pixels never cross into Go.
const NativeTextures = false
