package all

import (
	// NOTE (maestrohub fork): the upstream kvaser transport is intentionally
	// omitted. Up to v2.0.1 its Windows implementation (kvaser_windows.go)
	// imported github.com/samsamfire/gocanopen v1.x, which is GPL-3.0, and
	// registered into v1's transport registry. Upstream has since ported it to
	// v2 (08d78a67e), but this fork keeps it out: MaestroHub does not use it,
	// and leaving it out keeps the v1 module out of go.mod and go.sum. See
	// maestrohub issue #2662 and FORK.md.
	_ "github.com/maestrohub-labs/gocanopen/v2/pkg/can/socketcanring"
	_ "github.com/maestrohub-labs/gocanopen/v2/pkg/can/socketcanv2"
	_ "github.com/maestrohub-labs/gocanopen/v2/pkg/can/socketcanv3"
	_ "github.com/maestrohub-labs/gocanopen/v2/pkg/can/virtual"
)
