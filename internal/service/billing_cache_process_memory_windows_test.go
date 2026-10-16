//go:build windows

package service

import (
	"fmt"
	"syscall"
	"unsafe"
)

type processMemoryCounters struct {
	Size                       uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
	PrivateUsage               uintptr
}

func peakProcessRSSBytes() (uint64, error) {
	counters := processMemoryCounters{Size: uint32(unsafe.Sizeof(processMemoryCounters{}))}
	process, _, _ := syscall.NewLazyDLL("kernel32.dll").NewProc("GetCurrentProcess").Call()
	result, _, callErr := syscall.NewLazyDLL("psapi.dll").NewProc("GetProcessMemoryInfo").Call(
		process,
		uintptr(unsafe.Pointer(&counters)),
		uintptr(counters.Size),
	)
	if result == 0 {
		return 0, fmt.Errorf("GetProcessMemoryInfo failed: %w", callErr)
	}
	return uint64(counters.PeakWorkingSetSize), nil
}
