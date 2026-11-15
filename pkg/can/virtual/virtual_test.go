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

	received, err := vcan.Recv()
	require.NoError(t, err)
	assert.Equal(t, frame, *received)
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

	received, err := vcan.Recv()
	require.NoError(t, err)
	assert.Equal(t, first, *received)
	received, err = vcan.Recv()
	require.NoError(t, err)
	assert.Equal(t, second, *received)
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

	received, err := vcan.Recv()
	require.NoError(t, err)
	assert.Equal(t, frame, *received)
}

// A frame that the deadline cuts short is finished, so that the peer does not
// read the rest of it as the next frame's length
func TestSendFinishesAFrameCutShort(t *testing.T) {
	vcan, remote := newPipedVcan(t)

	first := canopen.Frame{ID: 0x190, Flags: 0, DLC: 2, Data: [8]byte{0x01, 0x02}}
	second := canopen.Frame{ID: 0x290, Flags: 0, DLC: 1, Data: [8]byte{0x03}}
	firstBytes, err := serializeFrame(first)
	require.NoError(t, err)
	secondBytes, err := serializeFrame(second)
	require.NoError(t, err)

	// The peer takes a few bytes of the first frame, then stalls for longer
	// than the send deadline
	_ = remote.SetReadDeadline(time.Now().Add(3 * time.Second))
	got := make(chan []byte, 1)
	go func() {
		buf := make([]byte, len(firstBytes)+len(secondBytes))
		_, _ = io.ReadFull(remote, buf[:3])
		time.Sleep(5 * sendTimeout)
		_, _ = io.ReadFull(remote, buf[3:])
		got <- buf
	}()

	require.NoError(t, vcan.Send(first))
	require.NoError(t, vcan.Send(second))
	select {
	case buf := <-got:
		assert.Equal(t, append(firstBytes, secondBytes...), buf)
	case <-time.After(5 * time.Second):
		t.Fatal("frames never reached the peer")
	}
}

// A frame that cannot be finished closes the connection rather than leaving
// the stream out of sync
func TestSendClosesTheConnectionWhenAFrameCannotBeFinished(t *testing.T) {
	vcan, remote := newPipedVcan(t)

	frame := canopen.Frame{ID: 0x190, Flags: 0, DLC: 2, Data: [8]byte{0x01, 0x02}}
	read := make(chan error, 1)
	go func() {
		buf := make([]byte, 3)
		_, _ = io.ReadFull(remote, buf)
		// Stall longer than frameTimeout, then the connection must be gone
		time.Sleep(frameTimeout + 200*time.Millisecond)
		_ = remote.SetReadDeadline(time.Now().Add(time.Second))
		_, err := remote.Read(make([]byte, 64))
		read <- err
	}()

	assert.Error(t, vcan.Send(frame))
	select {
	case err := <-read:
		assert.ErrorIs(t, err, io.EOF)
	case <-time.After(5 * time.Second):
		t.Fatal("the peer's read never returned")
	}
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
	type result struct {
		frame *canopen.Frame
		err   error
	}
	received := make(chan result, 1)
	go func() {
		frame, err := peer.Recv()
		received <- result{frame, err}
	}()
	require.NoError(t, vcan.Send(next))
	select {
	case r := <-received:
		require.NoError(t, r.err)
		assert.Equal(t, next, *r.frame)
	case <-time.After(5 * time.Second):
		t.Fatal("the next frame never arrived")
	}
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
