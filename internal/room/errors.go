package room

import (
	"errors"
	"fmt"
)

var (
	ErrRoomClosed    = errors.New("room is closed")
	ErrManagerClosed = errors.New("room manager is closed")
	ErrSlowClient    = errors.New("client send queue is full")
)

type CodedError struct {
	Code    string
	Message string
}

func (e *CodedError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}
