package outbox_test

import (
	"context"
	"time"

	"github.com/assurrussa/outbox/outbox"
	"github.com/assurrussa/outbox/shared/types"
)

// Existing consumer signatures remain identical through the public aliases.
var (
	_ func(*outbox.Service, context.Context, string, string, time.Time) (types.JobID, error) = (*outbox.Service).Put
	_ func(types.MessageID, string, string) types.MessageID                                  = outbox.FanoutDeliveryID
	_ outbox.JobID                                                                           = types.JobID{}
	_ types.JobID                                                                            = outbox.JobID{}
	_ outbox.MessageID                                                                       = types.MessageID{}
	_ types.MessageID                                                                        = outbox.MessageID{}
)
