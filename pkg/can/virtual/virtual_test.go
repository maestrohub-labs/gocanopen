package virtual

import (
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	canopen "github.com/maestrohub-labs/gocanopen/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// CAN server should be running for this to work

var VCAN_CHANNEL string = "localhost:18888"

func newVcan(channel string) *Bus {
	canBus, _ := NewVirtualCanBus(channel)
	vcan, _ := canBus.(*Bus)
	return vcan
}

func TestSendAndRecv(t *testing.T) {
	vcan1 := newVcan(VCAN_CHANNEL)
	vcan2 := newVcan(VCAN_CHANNEL)
	err1 := vcan1.Connect()
	err2 := vcan2.Connect()
	if err1 != nil || err2 != nil {
		t.Fatal("failed to connect", err1, err2)
	}
	defer vcan1.Disconnect()
	defer vcan2.Disconnect()
	// Send 100 frames from vcan 1 && read 100 frames from vcan2
	// Check order and value
	// This test can fail with bad network conditions
	// frame := can.Frame{ID: 0x111, Flags: 0, DLC: 8, Data: [8]byte{0, 1, 2, 3, 4, 5, 6, 7}}
	// for i := 0; i < 100; i++ {
	// 	frame.Data[0] = uint8(i)
	// 	vcan1.Send(frame)
	// }
	// for i := 0; i < 100; i++ {
	// 	frame, err := vcan2.Recv()
	// 	assert.Nil(t, err)
	// 	assert.Equal(t, uint8(i), frame.Data[0])
	// }
}

// Create a bus on top of an in memory connection, the other end is returned so
// that the test can control how the frames are segmented
func newPipedVcan(t *testing.T) (*Bus, net.Conn) {
	t.Helper()
	local, remote := net.Pipe()
	t.Cleanup(func() {
		local.Close()
		remote.Close()
	})
	vcan := newVcan("")
	vcan.conn = local
	return vcan, remote
}

// Receive until a whole frame arrives. Recv returns [ErrNoMsg] while a frame
// is only partly there; any other error fails the test.
func recvFrame(t *testing.T, vcan *Bus) *canopen.Frame {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		frame, err := vcan.Recv()
		if errors.Is(err, ErrNoMsg) {
			continue
		}
		require.NoError(t, err)
		return frame
	}
	t.Fatal("no whole frame arrived")
	return nil
}

// TCP is a stream, a frame can be delivered in several segments. A short read
// must not be treated as an error, doing so leaves the stream out of sync and
// permanently kills the reception routine.
func TestRecvSegmentedFrame(t *testing.T) {
	vcan, remote := newPipedVcan(t)

	frame := canopen.Frame{ID: 0x111, Flags: 0, DLC: 8, Data: [8]byte{0, 1, 2, 3, 4, 5, 6, 7}}
	frameBytes, err := serializeFrame(frame)
	require.NoError(t, err)

	// Worst case segmentation : one byte at a time, so every read is short
	go func() {
		for _, b := range frameBytes {
			_, _ = remote.Write([]byte{b})
		}
	}()

	assert.Equal(t, frame, *recvFrame(t, vcan))
}

// A frame that stops part way, for longer than any deadline, is kept and
// completed when the rest arrives, and the next frame is still in sync
func TestRecvKeepsAFrameThatStallsPartWay(t *testing.T) {
	vcan, remote := newPipedVcan(t)

	first := canopen.Frame{ID: 0x190, Flags: 0, DLC: 2, Data: [8]byte{0x01, 0x02}}
	second := canopen.Frame{ID: 0x290, Flags: 0, DLC: 1, Data: [8]byte{0x03}}
	firstBytes, err := serializeFrame(first)
	require.NoError(t, err)
	secondBytes, err := serializeFrame(second)
	require.NoError(t, err)

	go func() {
		_, _ = remote.Write(firstBytes[:5])
		// Longer than any deadline, upstream's 1 s frame deadline included
		time.Sleep(1300 * time.Millisecond)
		_, _ = remote.Write(append(firstBytes[5:], secondBytes...))
	}()

	assert.Equal(t, first, *recvFrame(t, vcan))
	assert.Equal(t, second, *recvFrame(t, vcan))
}

// Several frames can also be delivered inside a single segment, none of them
// should be lost
func TestRecvCoalescedFrames(t *testing.T) {
	vcan, remote := newPipedVcan(t)

	first := canopen.Frame{ID: 0x111, Flags: 0, DLC: 8, Data: [8]byte{0, 1, 2, 3, 4, 5, 6, 7}}
	second := canopen.Frame{ID: 0x222, Flags: 0, DLC: 2, Data: [8]byte{0xAA, 0xBB}}
	firstBytes, err := serializeFrame(first)
	require.NoError(t, err)
	secondBytes, err := serializeFrame(second)
	require.NoError(t, err)

	go func() {
		_, _ = remote.Write(append(firstBytes, secondBytes...))
	}()

	assert.Equal(t, first, *recvFrame(t, vcan))
	assert.Equal(t, second, *recvFrame(t, vcan))
}

// An idle connection is not an error, it must stay usable
func TestRecvNoMessage(t *testing.T) {
	vcan, remote := newPipedVcan(t)

	_, err := vcan.Recv()
	assert.ErrorIs(t, err, ErrNoMsg)

	// The connection is still usable afterwards
	frame := canopen.Frame{ID: 0x111, Flags: 0, DLC: 1, Data: [8]byte{0x42}}
	frameBytes, err := serializeFrame(frame)
	require.NoError(t, err)
	go func() {
		_, _ = remote.Write(frameBytes)
	}()

	assert.Equal(t, frame, *recvFrame(t, vcan))
}

// A frame that the send deadline cuts short is finished ahead of the next
// frame, so that the peer does not read the next frame's bytes as its rest
func TestSendFinishesAFrameCutShort(t *testing.T) {
	vcan, remote := newPipedVcan(t)
	peer := newVcan("")
	peer.conn = remote

	first := canopen.Frame{ID: 0x190, Flags: 0, DLC: 2, Data: [8]byte{0x01, 0x02}}
	second := canopen.Frame{ID: 0x290, Flags: 0, DLC: 1, Data: [8]byte{0x03}}

	// The peer takes a few bytes of the first frame, then stalls for longer
	// than the send deadline
	firstBytes, err := serializeFrame(first)
	require.NoError(t, err)
	head := make(chan struct{})
	go func() {
		_ = remote.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, _ = io.ReadFull(remote, make([]byte, 3))
		close(head)
	}()
	require.NoError(t, vcan.Send(first), "a frame that has started is not an error")
	<-head

	// Once the peer reads again, the rest of the first frame arrives before
	// the second, and the peer's view of the stream stays in sync
	got := make(chan *canopen.Frame, 2)
	go func() {
		peer.rxBuf = append(peer.rxBuf, firstBytes[:3]...)
		got <- recvFrame(t, peer)
		got <- recvFrame(t, peer)
	}()
	require.NoError(t, vcan.Send(second))
	assert.Equal(t, first, *<-got)
	assert.Equal(t, second, *<-got)
}

// A peer that stalls part way through a frame for long does not end the
// connection : frames meanwhile are dropped whole, and when the peer reads
// again the stream is still in sync
func TestSendKeepsTheStreamInSyncWhenThePeerStalls(t *testing.T) {
	vcan, remote := newPipedVcan(t)
	peer := newVcan("")
	peer.conn = remote

	first := canopen.Frame{ID: 0x190, Flags: 0, DLC: 2, Data: [8]byte{0x01, 0x02}}
	dropped := canopen.Frame{ID: 0x1A0, Flags: 0, DLC: 1, Data: [8]byte{0xEE}}
	next := canopen.Frame{ID: 0x290, Flags: 0, DLC: 1, Data: [8]byte{0x03}}
	firstBytes, err := serializeFrame(first)
	require.NoError(t, err)

	head := make(chan struct{})
	go func() {
		_ = remote.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, _ = io.ReadFull(remote, make([]byte, 3))
		close(head)
	}()
	require.NoError(t, vcan.Send(first))
	<-head

	// Nobody reads for well over a second : nothing more can start
	deadline := time.Now().Add(1200 * time.Millisecond)
	for time.Now().Before(deadline) {
		assert.Error(t, vcan.Send(dropped))
		time.Sleep(50 * time.Millisecond)
	}

	got := make(chan *canopen.Frame, 2)
	go func() {
		peer.rxBuf = append(peer.rxBuf, firstBytes[:3]...)
		got <- recvFrame(t, peer)
		got <- recvFrame(t, peer)
	}()
	require.NoError(t, vcan.Send(next))
	assert.Equal(t, first, *<-got)
	assert.Equal(t, next, *<-got)
}

// A frame that could not start before the deadline is dropped whole, the next
// one arrives intact
func TestSendDropsAFrameThatNeverStarted(t *testing.T) {
	vcan, remote := newPipedVcan(t)

	dropped := canopen.Frame{ID: 0x190, Flags: 0, DLC: 2, Data: [8]byte{0x01, 0x02}}
	next := canopen.Frame{ID: 0x290, Flags: 0, DLC: 1, Data: [8]byte{0x03}}

	// Nobody reads yet, so nothing of the first frame is sent
	assert.Error(t, vcan.Send(dropped))

	peer := newVcan("")
	peer.conn = remote
	sent := make(chan error, 1)
	go func() { sent <- vcan.Send(next) }()
	assert.Equal(t, next, *recvFrame(t, peer))
	require.NoError(t, <-sent)
}

// Frames sent from several go routines while the peer reads slowly all arrive
// whole and in sync, or not at all
func TestSendConcurrentFramesStayInSync(t *testing.T) {
	vcan, remote := newPipedVcan(t)
	peer := newVcan("")
	peer.conn = remote

	const senders, perSender = 8, 50
	var wg sync.WaitGroup
	for s := range senders {
		wg.Add(1)
		go func() {
			defer wg.Done()
			frame := canopen.Frame{ID: uint32(0x180 + s), Flags: 0, DLC: 8, Data: [8]byte{byte(s), 1, 2, 3, 4, 5, 6, 7}}
			for range perSender {
				_ = vcan.Send(frame)
			}
		}()
	}
	sent := make(chan struct{})
	go func() { wg.Wait(); close(sent) }()

	received := 0
	for {
		frame, err := peer.Recv()
		if errors.Is(err, ErrNoMsg) {
			select {
			case <-sent:
				assert.Positive(t, received)
				return
			default:
				continue
			}
		}
		require.NoError(t, err, "after %v frames", received)
		s := int(frame.ID) - 0x180
		require.True(t, s >= 0 && s < senders, "frame id %x", frame.ID)
		assert.Equal(t, [8]byte{byte(s), 1, 2, 3, 4, 5, 6, 7}, frame.Data)
		received++
		if received%25 == 0 {
			// A slow reader, so that some writes hit the deadline
			time.Sleep(3 * sendTimeout)
		}
	}
}

type FrameReceiver struct {
	mu     sync.Mutex
	frames []canopen.Frame
}

func (frameReceiver *FrameReceiver) Handle(frame canopen.Frame) {
	frameReceiver.mu.Lock()
	defer frameReceiver.mu.Unlock()
	frameReceiver.frames = append(frameReceiver.frames, frame)
}

func TestSendAndSubscribe(t *testing.T) {
	vcan1 := newVcan(VCAN_CHANNEL)
	vcan2 := newVcan(VCAN_CHANNEL)
	defer vcan1.Disconnect()
	defer vcan2.Disconnect()
	err1 := vcan1.Connect()
	err2 := vcan2.Connect()
	if err1 != nil || err2 != nil {
		t.Fatal("failed to connect", err1, err2)
	}
	frameReceiver := FrameReceiver{frames: make([]canopen.Frame, 0)}
	frameReceiver.mu.Lock()
	vcan2.Subscribe(&frameReceiver)
	frameReceiver.mu.Unlock()
	// Send 100 frames from vcan 1 && read 100 frames from vcan2
	// Check order and value
	frame := canopen.Frame{ID: 0x111, Flags: 0, DLC: 8, Data: [8]byte{0, 1, 2, 3, 4, 5, 6, 7}}
	for i := range 10 {
		frame.Data[0] = uint8(i)
		vcan1.Send(frame)
	}
	// Tiny sleep
	time.Sleep(time.Millisecond * 500)
	frameReceiver.mu.Lock()
	defer frameReceiver.mu.Unlock()
	// assert.GreaterOrEqual(t, len(frameReceiver.frames), 10)
	// for i, frame := range frameReceiver.frames {
	// 	assert.EqualValues(t, 0x111, frame.ID)
	// 	assert.EqualValues(t, uint8(i), frame.Data[0])
	// }
}

func TestReceiveOwn(t *testing.T) {
	vcan1 := newVcan(VCAN_CHANNEL)
	defer vcan1.Disconnect()
	frameReceiver := FrameReceiver{frames: make([]canopen.Frame, 0)}
	vcan1.Subscribe(&frameReceiver)
	frame := canopen.Frame{ID: 0x111, Flags: 0, DLC: 8, Data: [8]byte{0, 1, 2, 3, 4, 5, 6, 7}}
	vcan1.Send(frame)
	// Tiny sleep
	time.Sleep(time.Millisecond * 10)
	assert.Equal(t, len(frameReceiver.frames), 0)

	// Activate receive own
	vcan1.receiveOwn = true
	vcan1.Send(frame)
	assert.NotEqual(t, len(frameReceiver.frames), 0)
}
