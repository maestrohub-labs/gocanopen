# MaestroHub fork of `gocanopen/v2`

This is a fork of [`github.com/samsamfire/gocanopen`](https://github.com/samsamfire/gocanopen),
re-published as `github.com/maestrohub-labs/gocanopen/v2`.

## Base

Upstream `main` at `c76d0120325fb9ce2f1de1fa421708b990b58ff8`
("fix: send current UTC time from TIME producer", 2026-11-13), which is
upstream `v2.1.1` plus 10 commits. The fork's history starts from that commit:
our changes are the commits on top of it.

Earlier releases (`v2.0.1-mh.1` to `v2.0.1-mh.4`) were based on upstream
`v2.0.1`, with upstream's PDO and SYNC fixes cherry-picked. Upstream `main`
contains every one of those cherry-picks, so they are no longer carried.

## License

Upstream `gocanopen/v2` is **MIT licensed** (see `LICENSE`, Copyright (c) 2025
Samuel Lee). This fork retains that MIT license and copyright notice unchanged,
as MIT requires. Our modifications below are likewise MIT.

## Why this fork exists

Up to `v2.0.1`, the upstream `v2` module's CAN-transport aggregator
(`pkg/can/all/all.go`) blank-imported `pkg/can/kvaser`, whose Windows
implementation (`kvaser_windows.go`, `//go:build windows`) imported the **v1**
module `github.com/samsamfire/gocanopen`, which is licensed **GPL-3.0**. Any
Windows binary importing `pkg/network` pulled GPL-3.0 code in, which a
closed-source product distributed as a binary cannot ship. See MaestroHub issue
#2662 for the full analysis.

Upstream has since ported the Kvaser transport to v2 (`08d78a67e`): it no
longer imports v1. Upstream's `go.mod` still requires
`github.com/samsamfire/gocanopen v1.4.4` although no package imports it.

## What we carry

1. **Module path renamed** to `github.com/maestrohub-labs/gocanopen/v2`, in
   `go.mod` and every import. `go mod tidy` drops upstream's stale require on
   the v1 module: `github.com/samsamfire/gocanopen` (v1) is in neither `go.mod`
   nor `go.sum`.
2. **The `kvaser` transport is removed** (`pkg/can/kvaser/`), with its blank
   import in `pkg/can/all/all.go`. MaestroHub does not use it. Upstream's later
   Kvaser fixes (`08d78a67e`, `9217b91db`, `d4d89578e`, `6578b02ae`) are not
   carried.
3. **VirtualCAN stream** (`pkg/can/virtual`, first released in
   `v2.0.1-mh.2` and `v2.0.1-mh.3`). The TCP stream is read and written so that
   a split, coalesced or slow frame never ends the connection nor puts the
   stream out of sync:
   - `Recv` keeps a partly arrived frame in `rxBuf` (`popFrame`) and completes
     it on a later call, returning `ErrNoMsg` within the 200 ms poll. Upstream
     gives the rest of a frame 1 s, then ends the reception routine.
   - `writeFrame` sends under `sendMu`. The rest of a frame the 10 ms send
     deadline cut short is kept in `txPending` and written before any other
     frame. A frame that cannot start is dropped whole with an error. Upstream
     can cut a frame short, which puts the stream out of sync.
   - Tests: `TestRecvKeepsAFrameThatStallsPartWay`,
     `TestSendFinishesAFrameCutShort`,
     `TestSendKeepsTheStreamInSyncWhenThePeerStalls`,
     `TestSendDropsAFrameThatNeverStarted`,
     `TestSendConcurrentFramesStayInSync`, and the `recvFrame` helper.

## What diverges from upstream

Everything else is upstream `main` unchanged. These files differ:

| Path | Difference |
| --- | --- |
| `go.mod`, `go.sum` | module path; no v1 require |
| every `*.go` import | module path |
| `pkg/can/kvaser/` | removed |
| `pkg/can/all/all.go` | no kvaser import; fork note |
| `pkg/can/virtual/virtual.go`, `virtual_test.go` | our VirtualCAN stream changes |
| `FORK.md` | this file |

## Tests

The `pkg/network` tests need a virtualcan server on `localhost:18888`: any
server that relays each length-prefixed frame (a 4-byte big-endian length, then
the body) to every other client.

## Known upstream issues (not fixed here)

- `TestNmt/reset_comm_when_stopped` fails on upstream `main` itself since
  `14a1a9da6` ("fix: restart NMT on reset communication"): a node now restarts
  its NMT on reset communication and boots back to OPERATIONAL, while the test
  still expects it to stay STOPPED.
- `TestSyncConsumer` and `TestHBConfigurator` (`pkg/network`) fail under
  `-race`. The races are in the tests: a `bytes.Buffer` log sink read while the
  logger writes to it, and a package-level slice appended from the EMCY
  callback.
- In a block upload, the SDO client takes the size the server indicates from
  one byte (`GetBlockSize()`, `raw[4]`) instead of four (`raw[4:8]`). Reading an
  object of 256 bytes or more with block transfer (`SDOClient.ReadAll`) can
  then abort with `0x06070012` (length too high).

## Re-enabling Kvaser (future work)

Upstream's Kvaser transport now builds against v2 only. To re-enable it, take
upstream's `pkg/can/kvaser/` and its import in `pkg/can/all/all.go`, review it
for licensing again, and confirm the v1 module stays out of `go.mod` and
`go.sum`.

## Upgrading the fork

1. Branch from the new upstream commit.
2. Re-apply "What we carry": one commit for the rename and the Kvaser removal,
   then our VirtualCAN commits (cherry-pick them; upstream's `virtual.go` may
   have changed), then this file.
3. Run `go mod tidy` and confirm `github.com/samsamfire/gocanopen` (v1)
   appears in neither `go.mod` nor `go.sum`.
4. Run `go build ./...`, `go vet ./...`, `go test ./...` (with the relay), and
   `go test -race` on `pkg/can/virtual` and `pkg/network`.
5. Tag a new `v2.x.y-mh.N` release and bump the require in the maestrohub repo.
   Push only our own tags: the clone also holds upstream's tags.
