package all

import (
	// NOTE (maestrohub fork): the upstream kvaser transport is intentionally
	// omitted. Its Windows implementation (kvaser_windows.go) imports
	// github.com/samsamfire/gocanopen v1.x, which is GPL-3.0 — incompatible with
	// shipping a closed-source binary. The transport is also non-functional in
	// v2 regardless (it registers into v1's transport registry, which v2's
	// network.NewBus never reads), so dropping it removes no working capability.
	// See maestrohub issue #2662. Real Windows CAN-hardware support, if needed,
	// must be re-implemented against v2's own can registry/types (no v1 import).
	_ "github.com/maestrohub-labs/gocanopen/v2/pkg/can/socketcanring"
	_ "github.com/maestrohub-labs/gocanopen/v2/pkg/can/socketcanv2"
	_ "github.com/maestrohub-labs/gocanopen/v2/pkg/can/socketcanv3"
	_ "github.com/maestrohub-labs/gocanopen/v2/pkg/can/virtual"
)
