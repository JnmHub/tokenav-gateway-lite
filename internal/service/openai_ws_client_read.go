package service

import (
	"context"
	"errors"
	"time"

	coderws "github.com/coder/websocket"
)

type openAIWSClientReadResult struct {
	messageType coderws.MessageType
	payload     []byte
	err         error
}

// ReadOpenAIWSClientMessage keeps one reader alive while a timeout or lease
// cancellation sends the close frame, then closes the transport and joins the
// reader. Using Conn.Read with a deadline context directly can tear down the
// socket before the peer receives a protocol close frame, leaving clients with
// an opaque 1006 and leaking the blocked read goroutine until much later.
func ReadOpenAIWSClientMessage(
	controlCtx context.Context,
	conn *coderws.Conn,
	timeout time.Duration,
	timeoutStatus coderws.StatusCode,
	timeoutReason string,
) (coderws.MessageType, []byte, error) {
	if conn == nil {
		return 0, nil, errors.New("openai websocket client connection is nil")
	}
	if controlCtx == nil {
		controlCtx = context.Background()
	}

	readDone := make(chan openAIWSClientReadResult, 1)
	go func() {
		messageType, payload, err := conn.Read(context.Background())
		readDone <- openAIWSClientReadResult{messageType: messageType, payload: payload, err: err}
	}()

	var timeoutCh <-chan time.Time
	var timer *time.Timer
	if timeout > 0 {
		timer = time.NewTimer(timeout)
		timeoutCh = timer.C
		defer timer.Stop()
	}

	closeAndJoin := func(status coderws.StatusCode, reason string, cause error) (coderws.MessageType, []byte, error) {
		_ = conn.Close(status, reason)
		_ = conn.CloseNow()
		<-readDone
		return 0, nil, NewOpenAIWSClientCloseError(status, reason, cause)
	}

	select {
	case result := <-readDone:
		return result.messageType, result.payload, result.err
	case <-timeoutCh:
		return closeAndJoin(timeoutStatus, timeoutReason, context.DeadlineExceeded)
	case <-controlCtx.Done():
		cause := context.Cause(controlCtx)
		if errors.Is(cause, ErrOpenAIWSIngressLeaseLost) {
			return closeAndJoin(
				coderws.StatusTryAgainLater,
				"websocket ingress capacity lease lost; please reconnect",
				cause,
			)
		}
		return closeAndJoin(coderws.StatusGoingAway, "websocket request canceled", cause)
	}
}
