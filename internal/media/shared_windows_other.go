//go:build windows && !amd64 && !arm64

package media

func SetRendererAdapterLUID(low uint32, high int32) {}
func IsSharedTextureRetired(id uint64) bool         { return false }
func ReportSharedTextureError(id uint64, err error) {}
