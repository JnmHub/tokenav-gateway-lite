//go:build !windows && !linux

package service

import "fmt"

func peakProcessRSSBytes() (uint64, error) {
	return 0, fmt.Errorf("peak RSS is unsupported on this operating system")
}
