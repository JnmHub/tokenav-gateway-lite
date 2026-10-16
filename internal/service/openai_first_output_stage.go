package service

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync/atomic"
)

const (
	openAIFirstOutputStageMemoryLimit        = 64 * 1024
	openAIFirstOutputStageMaxBytes           = 8 * 1024 * 1024
	openAIFirstOutputScannerFramingAllowance = 64
	openAIFirstOutputGuardQueueSize          = 1
	openAIDefaultStreamQueueSize             = 16
)

var (
	errOpenAIFirstOutputStageLimit   = errors.New("openai first-output staging limit exceeded")
	errOpenAIFirstOutputScannerLimit = errors.New("openai pre-output scanner token limit exceeded")
)

// openAIFirstOutputStage keeps attempt-local SSE frames away from the client
// until the first meaningful event. This preserves safe account failover when
// an upstream emits large metadata or empty reasoning frames before failing.
type openAIFirstOutputStage struct {
	limit      int64
	size       int64
	memory     bytes.Buffer
	tempFile   *os.File
	tempPath   string
	createTemp func() (*os.File, error)
	removeFile func(string) error
	memoryOnly bool
	cleanupErr error
	closed     bool
}

func newOpenAIFirstOutputStage() *openAIFirstOutputStage {
	return &openAIFirstOutputStage{
		limit: openAIFirstOutputStageMaxBytes,
		createTemp: func() (*os.File, error) {
			return os.CreateTemp("", "sub2api-openai-first-output-*")
		},
		removeFile: os.Remove,
		memoryOnly: runtime.GOOS == "windows",
	}
}

func openAIFirstOutputEventQueueSize(guardFirstOutput bool) int {
	if guardFirstOutput {
		return openAIFirstOutputGuardQueueSize
	}
	return openAIDefaultStreamQueueSize
}

// 首个语义输出前限制 Scanner（扫描器）单个 token 的体积，避免全局较大的
// max_line_size 配置和异步队列叠加后，把一个异常上游帧放大为数百 MiB 内存占用。
func openAIFirstOutputDynamicScanLines(guardActive *atomic.Bool) bufio.SplitFunc {
	return func(data []byte, atEOF bool) (advance int, token []byte, err error) {
		advance, token, err = bufio.ScanLines(data, atEOF)
		if err != nil || guardActive == nil || !guardActive.Load() {
			return advance, token, err
		}
		limit := openAIFirstOutputStageMaxBytes + openAIFirstOutputScannerFramingAllowance
		if token != nil {
			if len(token) > limit {
				return 0, nil, errOpenAIFirstOutputScannerLimit
			}
			return advance, token, nil
		}
		if len(data) >= limit {
			return 0, nil, errOpenAIFirstOutputScannerLimit
		}
		return advance, token, nil
	}
}

func (s *openAIFirstOutputStage) Buffered() int64 {
	if s == nil {
		return 0
	}
	return s.size
}

func (s *openAIFirstOutputStage) WriteString(value string) (int, error) {
	if err := s.prepareWrite(len(value)); err != nil {
		return 0, err
	}
	var (
		n   int
		err error
	)
	if s.tempFile != nil {
		n, err = io.WriteString(s.tempFile, value)
	} else {
		n, err = s.memory.WriteString(value)
	}
	s.size += int64(n)
	if err != nil {
		return n, fmt.Errorf("write first-output stage: %w", err)
	}
	return n, nil
}

func (s *openAIFirstOutputStage) prepareWrite(incoming int) error {
	if s == nil || s.closed {
		return os.ErrClosed
	}
	if int64(incoming) > s.limit-s.size {
		return fmt.Errorf("%w: buffered=%d incoming=%d limit=%d", errOpenAIFirstOutputStageLimit, s.size, incoming, s.limit)
	}
	if s.tempFile != nil || s.memoryOnly || s.size+int64(incoming) <= openAIFirstOutputStageMemoryLimit {
		return nil
	}
	file, err := s.createTemp()
	if err != nil {
		return fmt.Errorf("create first-output spool: %w", err)
	}
	path := file.Name()
	// 在写入任何上游内容前先取消文件名；Unix 仍可通过描述符读取，
	// 进程崩溃时则不会遗留带明文思考内容的临时文件。
	if unlinkErr := s.removeFile(path); unlinkErr != nil {
		closeErr := file.Close()
		removeErr := s.removeFile(path)
		if errors.Is(removeErr, os.ErrNotExist) {
			removeErr = nil
		}
		s.memoryOnly = true
		if removeErr != nil {
			s.tempPath = path
		}
		s.cleanupErr = errors.Join(
			s.cleanupErr,
			fmt.Errorf("unlink first-output spool before use: %w", unlinkErr),
			closeErr,
			removeErr,
		)
		return nil
	}
	if _, err := file.Write(s.memory.Bytes()); err != nil {
		_ = file.Close()
		return fmt.Errorf("initialize first-output spool: %w", err)
	}
	s.tempFile = file
	// 已成功 unlink（取消文件名），只能通过当前描述符访问；不要在 Close
	// 再按旧路径删除，避免极小概率下路径被系统复用后误删其他临时文件。
	s.tempPath = ""
	s.memory.Reset()
	return nil
}

func (s *openAIFirstOutputStage) CommitTo(dst io.Writer) error {
	if s == nil || s.closed {
		return os.ErrClosed
	}
	if s.tempFile == nil {
		if _, err := io.Copy(dst, bytes.NewReader(s.memory.Bytes())); err != nil {
			return err
		}
	} else {
		if _, err := s.tempFile.Seek(0, io.SeekStart); err != nil {
			return fmt.Errorf("seek first-output spool: %w", err)
		}
		if _, err := io.CopyN(dst, s.tempFile, s.size); err != nil {
			return err
		}
	}
	if err := s.Close(); err != nil {
		// 内容已成功交付，清理失败只留给 defer 再尝试和记录，不能把已经
		// 写给客户端的字节误报成可换号的流失败。
		s.cleanupErr = errors.Join(s.cleanupErr, err)
	}
	return nil
}

func (s *openAIFirstOutputStage) Close() error {
	if s == nil {
		return nil
	}
	if s.closed && s.tempFile == nil && s.tempPath == "" && s.cleanupErr == nil {
		return nil
	}
	s.closed = true
	s.size = 0
	s.memory.Reset()
	closeErr := s.cleanupErr
	s.cleanupErr = nil
	if s.tempFile != nil {
		closeErr = errors.Join(closeErr, s.tempFile.Close())
		s.tempFile = nil
	}
	if s.tempPath != "" {
		removeErr := s.removeFile(s.tempPath)
		if removeErr == nil || errors.Is(removeErr, os.ErrNotExist) {
			s.tempPath = ""
		} else {
			closeErr = errors.Join(closeErr, removeErr)
		}
	}
	return closeErr
}
