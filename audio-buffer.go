package main

import (
	"io"
	"sync"
)

type audioBuffer struct {
	buf    []byte
	mutex  sync.Mutex
	cond   *sync.Cond
	closed bool

	// Buffer configuration
	capacity  int
	lowWater  int // When to wait for more data
	highWater int // When to start dropping data
}

func newAudioBuffer() *audioBuffer {
	const (
		// 48000 samples/sec * 2 channels * 2 bytes/sample = 192000 bytes/sec
		bytesPerSecond = 48000 * 2 * 2

		// Buffer capacity (500 ms)
		bufferCapacity = bytesPerSecond / 2

		// Low water mark (50ms)
		lowWaterMark = bytesPerSecond / 20

		// High water mark (400ms)
		highWaterMark = (bufferCapacity * 4) / 5
	)

	b := &audioBuffer{
		capacity:  bufferCapacity,
		lowWater:  lowWaterMark,
		highWater: highWaterMark,
	}
	b.buf = make([]byte, 0, b.capacity)
	b.cond = sync.NewCond(&b.mutex)
	return b
}

func (b *audioBuffer) Read(buf []byte) (n int, err error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	// Wait for enough data or until closed
	for len(b.buf) < b.lowWater && !b.closed {
		b.cond.Wait()
	}

	if b.closed && len(b.buf) == 0 {
		return 0, io.EOF
	}

	// Read what we can
	n = copy(buf, b.buf)
	b.buf = b.buf[n:]

	// If buffer is getting low, signal writer
	if len(b.buf) < b.lowWater {
		b.cond.Signal()
	}

	return n, nil
}

func (b *audioBuffer) Write(data []byte) (n int, err error) {
	if len(data) == 0 {
		return 0, nil
	}

	b.mutex.Lock()
	defer b.mutex.Unlock()

	if b.closed {
		return 0, io.ErrClosedPipe
	}

	// If buffer would overflow high water mark, drop oldest data
	for len(b.buf)+len(data) > b.highWater {
		dropSize := len(data)
		if dropSize > len(b.buf) {
			dropSize = len(b.buf)
		}
		b.buf = b.buf[dropSize:]
	}

	// Append new data
	b.buf = append(b.buf, data...)

	// Signal reader if we've reached low water mark
	if len(b.buf) >= b.lowWater {
		b.cond.Signal()
	}

	return len(data), nil
}

func (b *audioBuffer) Close() error {
	b.mutex.Lock()
	defer b.mutex.Unlock()

	if !b.closed {
		b.closed = true
		b.cond.Broadcast()
	}
	return nil
}
