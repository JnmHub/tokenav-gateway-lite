//go:build linux

package service

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func peakProcessRSSBytes() (uint64, error) {
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return 0, err
	}
	for _, line := range strings.Split(string(status), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[0] == "VmHWM:" {
			kilobytes, parseErr := strconv.ParseUint(fields[1], 10, 64)
			if parseErr != nil {
				return 0, parseErr
			}
			return kilobytes * 1024, nil
		}
	}
	return 0, fmt.Errorf("VmHWM is absent from /proc/self/status")
}
