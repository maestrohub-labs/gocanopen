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

## Fixes carried beyond upstream `v2.0.1`

### VirtualCAN stream (`v2.0.1-mh.2`, `v2.0.1-mh.3`)

`pkg/can/virtual` reads and writes the TCP stream so that a split, coalesced or
slow frame never ends the connection nor puts the stream out of sync.

- Upstream's reception fix (`06e9969e2`) is included.
- On top of it, our own changes:
  - Frames are sent whole, under a send lock.
  - The rest of a frame the send deadline cut short is written before any other frame.
  - The reader keeps a partly arrived frame for its next call, instead of
    upstream's 1 s frame deadline, which ends the reception routine.

This part **diverges from upstream**: upstream still has the 1 s frame deadline
and a send deadline that can cut frames short. When upgrading, keep our
`Recv`/`writeFrame` and their tests (`TestRecvKeepsAFrameThatStallsPartWay`,
`TestSendFinishesAFrameCutShort`, `TestSendKeepsTheStreamInSyncWhenThePeerStalls`).

### PDO and SYNC fixes from upstream (`v2.0.1-mh.4`)

Every upstream commit since `v2.0.1` that touches `pkg/pdo` or `pkg/sync`,
cherry-picked in order (`-x`, import paths rewritten). They include the fix for
a data race between `TPDO.Stop` and the SYNC handler (`1f72e2a37`). They also
bring the regression tests these fixes came with, in `pkg/network/pdo_test.go`.

- `pkg/pdo`, `pkg/sync` and `pkg/network/pdo_test.go` are identical to upstream
  at `b7559c4c4`, apart from the import path.
- Two fixes from outside those packages are included because the PDO tests need
  them: `96e2cbb63` (`pkg/od/streamer.go`, clearing a mapping with dummy entries)
  and `76a13ceb6` (`pkg/emergency`, error status bits).
- `eaed4d99f` is included for its `pkg/sync` part only. Its `pkg/nmt` and
  `pkg/time` parts build on upstream NMT and TIME changes this fork does not
  carry.

Not taken: upstream's other fixes (SDO block transfers, chunked OD access, NMT,
TIME, EMCY producer) and its event-driven SDO client. Take them with a move to a
newer upstream release.

The network tests need a virtualcan server on `localhost:18888`: any server that
relays each length-prefixed frame to the other clients.

## Re-enabling real Windows CAN hardware (future work)

If real CAN-hardware support on Windows is needed, re-implement the Kvaser (or
PCAN, etc.) transport against **v2's own** `pkg/can` registry and `Bus`/`Frame`
types — without importing the GPL-3.0 v1 module — and re-add it to
`pkg/can/all/all.go` under a `//go:build windows` tag.

## Upgrading the fork

When bumping to a newer upstream `gocanopen/v2`:
1. Re-apply the two changes above, and our VirtualCAN stream changes
   (see "Fixes carried beyond upstream").
2. Re-run `go mod tidy` and confirm `github.com/samsamfire/gocanopen` (v1)
   appears in neither `go.mod` nor `go.sum`.
3. Tag a new `v2.x.y-mh.N` release and bump the require in the maestrohub repo.
