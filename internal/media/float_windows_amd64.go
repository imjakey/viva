//go:build windows

package media

import (
	"math"
	"syscall"
)

// A COM method whose argument or result is a double needs the Windows
// floating point calling convention, which syscall.SyscallN does not set up
// on its own. On amd64 it relies on Go's runtime asmstdcall implementation
// (verified on Go 1.22+), which mirrors the first four integer arguments into
// XMM0–XMM3 before the call and returns XMM0 in the second result register r2.
// Thus passing math.Float64bits(v) in the second slot puts v in XMM1, and a
// double return value in XMM0 arrives as the second result of SyscallN.

func initFloatThunks() error { return nil }

// callWithDouble calls method i of obj with v as its double argument.
func callWithDouble(obj uintptr, i int, v float64) uintptr {
	hr, _, _ := syscall.SyscallN(vfunc(obj, i), obj, uintptr(math.Float64bits(v)))
	return hr
}

// callDouble calls method i of obj, which returns a double.
func callDouble(obj uintptr, i int) float64 {
	_, bits, _ := syscall.SyscallN(vfunc(obj, i), obj)
	return math.Float64frombits(uint64(bits))
}
