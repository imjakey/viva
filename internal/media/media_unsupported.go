//go:build !((windows && (amd64 || arm64)) || (darwin && cgo && (amd64 || arm64)))

package media

// Open opens a video with the system's media framework. This build of the
// package has no engine: Open reports ErrUnsupported, which an application
// may answer by playing a GIF instead.
func Open(s Source) (Engine, error) {
	return nil, ErrUnsupported
}
