package transporthttp

import (
	"errors"
	"net/http"
	"time"
)

type (
	JSONUnmarshaler func(data []byte, v any) error
	Option          func(*Options)
)

type Options struct {
	timeout   time.Duration
	unmarshal JSONUnmarshaler
	client    *http.Client
}

func WithTimeout(opt time.Duration) Option {
	return func(o *Options) { o.timeout = opt }
}

func WithUnmarshal(opt JSONUnmarshaler) Option {
	return func(o *Options) { o.unmarshal = opt }
}

func WithClient(opt *http.Client) Option {
	return func(o *Options) { o.client = opt }
}

func (o *Options) Validate() error {
	if o == nil {
		return errors.New("nil options")
	}
	if o.unmarshal == nil {
		return errors.New("nil JSONUnmarshaler")
	}
	if o.timeout < 1*time.Second {
		return errors.New("timeout must be at least 1 second")
	}
	if o.timeout > 5*time.Minute {
		return errors.New("timeout must be at most 5 minutes")
	}

	return nil
}
