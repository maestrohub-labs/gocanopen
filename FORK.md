# MaestroHub fork of `gocanopen/v2`

This is a fork of [`github.com/samsamfire/gocanopen`](https://github.com/samsamfire/gocanopen)
`v2.0.1`, re-published as `github.com/maestrohub-labs/gocanopen/v2`.

## License

Upstream `gocanopen/v2` is **MIT licensed** (see `LICENSE`, Copyright (c) 2025
Samuel Lee). This fork retains that MIT license and copyright notice unchanged,
as MIT requires. Our modifications below are likewise MIT.

## Why this fork exists

The upstream `v2` module's CAN-transport aggregator
(`pkg/can/all/all.go`) blank-imports `pkg/can/kvaser`. The Kvaser transport's
Windows implementation (`pkg/can/kvaser/kvaser_windows.go`, build-tagged
`//go:build windows`) imports the **v1** module
`github.com/samsamfire/gocanopen`, which is licensed **GPL-3.0**.

As a result, any binary built for Windows that transitively imports
`pkg/network` (which `all` is imported by) pulls GPL-3.0 code into the binary.
For a closed-source commercial product distributed as a downloadable binary,
this is a licensing violation. Linux/macOS builds are unaffected because the
non-Windows Kvaser file (`kvaser.go`, `//go:build !windows`) is an empty stub.

See MaestroHub issue #2662 for the full analysis.

## Changes from upstream `v2.0.1`

1. **Module path renamed** to `github.com/maestrohub-labs/gocanopen/v2`
   (all internal import paths updated accordingly).
2. **Removed the `kvaser` transport package** (`pkg/can/kvaser/`) and dropped
   its blank import from `pkg/can/all/all.go`. This eliminates the only path
   through which the GPL-3.0 `gocanopen` v1 module entered the build graph.

   This removes **no working capability**: in upstream `v2`, the Kvaser
   transport registers itself into the **v1** transport registry
   (`github.com/samsamfire/gocanopen/pkg/can`), while `v2`'s
   `network.NewBus` only ever reads the **v2** registry
   (`.../v2/pkg/can`). The two are distinct maps in distinct modules, and the
   Kvaser constructor returns a v1 `Bus` type that is not assignable to v2's.
   `NewBus("kvaser", …)` in upstream `v2` therefore returns
   `"not enabled: kvaser, check build flags"` — i.e. the transport never
   functioned in `v2` on any platform.

## Re-enabling real Windows CAN hardware (future work)

If real CAN-hardware support on Windows is needed, re-implement the Kvaser (or
PCAN, etc.) transport against **v2's own** `pkg/can` registry and `Bus`/`Frame`
types — without importing the GPL-3.0 v1 module — and re-add it to
`pkg/can/all/all.go` under a `//go:build windows` tag.

## Upgrading the fork

When bumping to a newer upstream `gocanopen/v2`:
1. Re-apply the two changes above.
2. Re-run `go mod tidy` and confirm `github.com/samsamfire/gocanopen` (v1)
   appears in neither `go.mod` nor `go.sum`.
3. Tag a new `v2.x.y-mh.N` release and bump the require in the maestrohub repo.
