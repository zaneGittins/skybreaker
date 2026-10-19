//go:build nodeps

package main

import (
	"context"

	"github.com/zaneGittins/skybreaker/internal/tsauth"
)

// A -tags nodeps build has no third-party code: JSON configs and loopback
// development mode only (tailscale.disabled: true).
var tailnet func(ctx context.Context, hostname, stateDir string) (tsauth.Network, error)
