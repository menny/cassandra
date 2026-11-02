package worker

import (
	"context"
	"time"

	"github.com/menny/cassandra/core/eval/testdata/cases/unupdated-caller-blast-radius/base/pkg/config"
)

// WithWorkerTimeout wraps ctx with a timeout parsed from rawTimeout.
func WithWorkerTimeout(ctx context.Context, rawTimeout string) (context.Context, context.CancelFunc, error) {
	sec, err := config.ParseTimeout(rawTimeout)
	if err != nil {
		return nil, nil, err
	}
	tCtx, cancel := context.WithTimeout(ctx, time.Duration(sec)*time.Second)
	return tCtx, cancel, nil
}
