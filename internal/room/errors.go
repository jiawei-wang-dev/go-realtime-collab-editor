package room

import "fmt"

type CodedError struct {
	Code    string
	Message string
}

func (e *CodedError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}
