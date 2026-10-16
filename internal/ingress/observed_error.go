package ingress

import "errors"

// observedBodyTooLargeError 表示正文超限已由入口 reader 聚合过。它仍通过
// Unwrap 保留原始 *http.MaxBytesError，协议 handler 可以继续返回原有 413。
type observedBodyTooLargeError struct{ err error }

func (e *observedBodyTooLargeError) Error() string { return e.err.Error() }
func (e *observedBodyTooLargeError) Unwrap() error { return e.err }

// MarkBodyTooLargeObserved 给错误链加匿名标记，不携带正文、Key 或请求头。
func MarkBodyTooLargeObserved(err error) error {
	if err == nil || BodyTooLargeObserved(err) {
		return err
	}
	return &observedBodyTooLargeError{err: err}
}

// BodyTooLargeObserved 用于避免中间件与协议 handler 对同一拒绝重复计数。
func BodyTooLargeObserved(err error) bool {
	var marker *observedBodyTooLargeError
	return errors.As(err, &marker)
}
