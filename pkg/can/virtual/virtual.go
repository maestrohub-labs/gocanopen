package virtual

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"time"

	canopen "github.com/maestrohub-labs/gocanopen/v2"
	can "github.com/maestrohub-labs/gocanopen/v2/pkg/can"
)

// Virtual CAN bus implementation with TCP primarily used for testing
// This needs a broker server to send CAN frames to all connected clients
// More information : https://github.com/windelbouwman/virtualcan

func init() {
	can.RegisterInterface("virtual", NewVirtualCanBus)
	can.RegisterInterface("virtualcan", NewVirtualCanBus)
}

const (
	// How long to wait for a new frame
	pollTimeout = 200 * time.Millisecond
	// How long to wait for a frame to start being sent
	sendTimeout = 10 * time.Millisecond
	// How long to wait before trying to take the lock again
	lockRetryDelay = 1 * time.Millisecond
	// Sanity check on the announced frame length, a serialized frame is 14 bytes
	maxFrameLength = 64
)

// Returned when no message was received before the polling deadline
var ErrNoMsg = errors.New("no message")

type Bus struct {
	logger        *slog.Logger
	mu            sync.Mutex
	sendMu        sync.Mutex
	channel       string
	conn          net.Conn
	receiveOwn    bool
	framehandler  canopen.FrameListener
	stopChan      chan bool
	wg            sync.WaitGroup
	isRunning     bool
	errSubscriber bool
	// Bytes received that do not make a whole frame yet
	rxBuf   []byte
	rxChunk [256]byte
	// The rest of a frame the send deadline cut short, sent before any other
	txPending []byte
}

func NewVirtualCanBus(channel string) (canopen.Bus, error) {
	return &Bus{
		channel:   channel,
		logger:    slog.Default(),
		stopChan:  make(chan bool),
		isRunning: false}, nil
}

// Helper function for serializing a CAN frame into the expected binary format
func serializeFrame(frame canopen.Frame) ([]byte, error) {
	buffer := new(bytes.Buffer)
	err := binary.Write(buffer, binary.BigEndian, frame)
	if err != nil {
		return nil, err
	}
	dataBytes := buffer.Bytes()
	frameBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(frameBytes, uint32(len(dataBytes)))
	frameBytes = append(frameBytes, dataBytes...)
	return frameBytes, nil
}

// Helper function for deserializing a CAN frame from expected binary format
func deserializeFrame(buffer []byte) (*canopen.Frame, error) {
	var frame canopen.Frame
	buf := bytes.NewBuffer(buffer)
	err := binary.Read(buf, binary.BigEndian, &frame)
	if err != nil {
		return nil, err
	}
	return &frame, nil
}

// "Connect" to server e.g. localhost:18000
func (b *Bus) Connect(...any) error {
	conn, err := net.Dial("tcp", b.channel)
	if err != nil {
		return err
	}
	b.conn = conn
	b.rxBuf = b.rxBuf[:0]
	b.txPending = b.txPending[:0]
	if tcpConn, ok := conn.(*net.TCPConn); ok {
		err := tcpConn.SetNoDelay(true)
		if err != nil {
			return err
		}
	}
	return nil
}

// "Disconnect" from server
func (b *Bus) Disconnect() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.errSubscriber && b.isRunning {
		b.stopChan <- true
		b.wg.Wait()
	}
	if b.conn != nil {
		return b.conn.Close()
	}
	return nil
}

// "Send" implementation of Bus interface
func (b *Bus) Send(frame canopen.Frame) error {
	// Local loopback
	if b.receiveOwn && b.framehandler != nil {
		b.framehandler.Handle(frame)
	} else if b.conn == nil {
		return errors.New("error : no active connection, abort send")
	}
	if b.conn != nil {
		frameBytes, err := serializeFrame(frame)
		if err != nil {
			return err
		}
		return b.writeFrame(frameBytes)
	}
	return nil
}

// Write one serialized frame without ever putting the stream out of sync.
// TCP is a stream : a frame cut short by the send deadline would make the peer
// read the next frame's bytes as the rest of it. So the rest of a frame cut
// short is kept, and written before any other frame : a frame is never
// interleaved with another nor left partial, and a slow peer never ends the
// connection. A frame that cannot start (the peer is not reading, or an
// earlier frame is still being finished) is dropped whole with an error.
// sendMu keeps frames sent from different go routines in order.
func (b *Bus) writeFrame(frameBytes []byte) error {
	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	_ = b.conn.SetWriteDeadline(time.Now().Add(sendTimeout))
	if len(b.txPending) > 0 {
		n, err := b.conn.Write(b.txPending)
		b.txPending = b.txPending[n:]
		if err != nil {
			return fmt.Errorf("an earlier frame is still being sent, frame dropped : %w", err)
		}
	}
	n, err := b.conn.Write(frameBytes)
	if err != nil && n > 0 {
		// Started : the rest goes out ahead of the next frame
		b.txPending = append(b.txPending[:0], frameBytes[n:]...)
		return nil
	}
	return err
}

// "Subscribe" implementation of Bus interface
func (b *Bus) Subscribe(framehandler canopen.FrameListener) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.framehandler = framehandler
	if b.isRunning {
		return nil
	}
	// Start go routine that receives incoming traffic and passes it to frameHandler
	b.wg.Add(1)
	b.isRunning = true
	b.errSubscriber = false
	go b.handleReception()
	return nil
}

// Receive new CAN message. [ErrNoMsg] is returned when no whole frame was
// received before the polling deadline, which is not an error condition.
//
// TCP is a stream : a frame can arrive split in several segments, or late
// part way through, and several frames can arrive in one segment. Bytes that
// do not make a whole frame yet are kept for the next call, so a split or slow
// frame never ends the reception routine nor puts the stream out of sync, and
// each call still returns within the polling deadline.
func (b *Bus) Recv() (*canopen.Frame, error) {
	if b.conn == nil {
		return nil, fmt.Errorf("error : no active connection, abort receive")
	}
	if frame, err := b.popFrame(); frame != nil || err != nil {
		return frame, err
	}
	_ = b.conn.SetReadDeadline(time.Now().Add(pollTimeout))
	n, err := b.conn.Read(b.rxChunk[:])
	b.rxBuf = append(b.rxBuf, b.rxChunk[:n]...)
	if frame, perr := b.popFrame(); frame != nil || perr != nil {
		return frame, perr
	}
	if err == nil {
		// Part of a frame, the rest comes with a later call
		return nil, ErrNoMsg
	}
	if netErr, ok := err.(net.Error); ok && netErr.Timeout() {
		return nil, ErrNoMsg
	}
	return nil, err
}

// Take one whole frame off the front of the receive buffer, nil if there is
// none yet
func (b *Bus) popFrame() (*canopen.Frame, error) {
	if len(b.rxBuf) < 4 {
		return nil, nil
	}
	length := binary.BigEndian.Uint32(b.rxBuf[:4])
	if length > maxFrameLength {
		return nil, fmt.Errorf("announced frame length %v exceeds %v", length, maxFrameLength)
	}
	end := 4 + int(length)
	if len(b.rxBuf) < end {
		return nil, nil
	}
	frame, err := deserializeFrame(b.rxBuf[4:end])
	b.rxBuf = b.rxBuf[end:]
	if err != nil {
		return nil, err
	}
	return frame, nil
}

// Handle incoming traffic
func (client *Bus) handleReception() {
	defer func() {
		client.isRunning = false
		client.wg.Done()
	}()
	for {
		select {
		case <-client.stopChan:
			return
		default:
			// Avoid blocking if lock is already taken (in particular for disconnect, subscribe, etc)
			if !client.mu.TryLock() {
				time.Sleep(lockRetryDelay)
				continue
			}
			frame, err := client.Recv()
			switch {
			case errors.Is(err, ErrNoMsg):
				// No message received
			case err != nil:
				client.logger.Error("listening routine has closed because", "err", err)
				client.errSubscriber = true
				client.mu.Unlock()
				return
			case client.framehandler != nil:
				client.framehandler.Handle(*frame)
			}
			client.mu.Unlock()
		}
	}
}

func (b *Bus) SetReceiveOwn(receiveOwn bool) {
	b.receiveOwn = receiveOwn
}
