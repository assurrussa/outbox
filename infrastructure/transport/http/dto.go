package transporthttp

import "io"

type RequestHeaders map[string]string

type Request struct {
	ExpectStatusCode   int
	ReadResponseAlways bool
	Method             string
	URL                string
	Body               io.Reader
	Headers            RequestHeaders
}
