//go:build windows

package media

import (
	"fmt"
	"math"
	"syscall"
	"unsafe"
)

// A COM method whose argument or result is a double needs the Windows
// floating point calling convention: the argument in D0, the result in D0.
// syscall.SyscallN works in the integer registers only, so on arm64 the call
// goes through one of two thunks of machine code, which move the bits of a
// double between D0 and the registers syscall uses:
//
//	withDouble(obj X0, bits X1, fn X2): FMOV D0, X1; BR X2
//	double(obj X0, fn X1): STP X29, X30, [SP, #-16]!; MOV X29, SP;
//	                       BLR X1; FMOV X0, D0; LDP X29, X30, [SP], #16; RET
//
// The thunks are written into a page of memory of their own, made executable
// once, at load.

var (
	withDoubleThunk, doubleThunk uintptr
)

func initFloatThunks() error {
	setter := []uint32{
		0x9e670020, // FMOV D0, X1
		0xd61f0040, // BR X2
	}
	getter := []uint32{
		0xa9bf7bfd, // STP X29, X30, [SP, #-16]!
		0x910003fd, // MOV X29, SP
		0xd63f0020, // BLR X1
		0x9e660000, // FMOV X0, D0
		0xa8c17bfd, // LDP X29, X30, [SP], #16
		0xd65f03c0, // RET
	}
	code := append(setter, getter...)
	kernel := syscall.NewLazyDLL("kernel32.dll")
	const commitReserve, pageReadWrite, pageExecuteRead = 0x3000, 0x04, 0x20
	mem, _, err := kernel.NewProc("VirtualAlloc").Call(0, uintptr(4*len(code)), commitReserve, pageReadWrite)
	if mem == 0 {
		return fmt.Errorf("video: VirtualAlloc: %v", err)
	}
	copy(unsafe.Slice((*uint32)(ptrAt(mem)), len(code)), code)
	var previous uint32
	if ok, _, err := kernel.NewProc("VirtualProtect").Call(mem, uintptr(4*len(code)), pageExecuteRead, uintptr(unsafe.Pointer(&previous))); ok == 0 {
		return fmt.Errorf("video: VirtualProtect: %v", err)
	}
	process, _, _ := kernel.NewProc("GetCurrentProcess").Call()
	kernel.NewProc("FlushInstructionCache").Call(process, mem, uintptr(4*len(code)))
	withDoubleThunk = mem
	doubleThunk = mem + uintptr(4*len(setter))
	return nil
}

// callWithDouble calls method i of obj with v as its double argument.
func callWithDouble(obj uintptr, i int, v float64) uintptr {
	hr, _, _ := syscall.SyscallN(withDoubleThunk, obj, uintptr(math.Float64bits(v)), vfunc(obj, i))
	return hr
}

// callDouble calls method i of obj, which returns a double.
func callDouble(obj uintptr, i int) float64 {
	bits, _, _ := syscall.SyscallN(doubleThunk, obj, vfunc(obj, i))
	return math.Float64frombits(uint64(bits))
}
