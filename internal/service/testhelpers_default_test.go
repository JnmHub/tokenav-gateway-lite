//go:build !unit

package service

func testPtrFloat64(v float64) *float64 { return &v }

func testPtrInt(v int) *int { return &v }

func testPtrString(v string) *string { return &v }

func testPtrBool(v bool) *bool { return &v }
