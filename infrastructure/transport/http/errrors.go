package transporthttp

import "errors"

var (
	ErrCreateRequest = errors.New("can't create new request")
	ErrSendRequest   = errors.New("can't send request")
	ErrNotFound      = errors.New("not found")
	ErrReadResponse  = errors.New("can't read response")
)
