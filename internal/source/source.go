// Package source defines where block heights come from: the watched node and
// the independent references that tell where the chain tip is.
package source

import (
	"context"
	"fmt"
)

// MaxHeight bounds any height we accept. It is far above any real chain
// (Bitcoin ~1e6, Ethereum ~3e7) and rejects garbage from a broken source.
const MaxHeight = 1_000_000_000

// Height is what a source reports.
type Height struct {
	Blocks  uint64 // fully validated blocks
	Headers uint64 // best known header; equal to Blocks if the source has no such notion
}

// Source reports a block height. Implementations must be safe for
// concurrent use and must honour ctx for cancellation and timeouts.
type Source interface {
	Name() string
	Height(ctx context.Context) (Height, error)
}

// Check validates a parsed height.
func Check(h int64) (uint64, error) {
	if h < 0 || h > MaxHeight {
		return 0, fmt.Errorf("implausible height %d", h)
	}
	return uint64(h), nil
}
