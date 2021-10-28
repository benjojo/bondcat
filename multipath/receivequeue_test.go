package multipath

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestRead(t *testing.T) {
	q := newReceiveQueue(2)
	fn := uint64(minFrameNumber - 1)
	addFrame := func(s string) {
		fn++
		q.add(&rxFrame{fn: fn, bytes: []byte(s)}, nil)
	}
	shouldRead := func(s string) {
		b := make([]byte, 3)
		n, err := q.read(b)
		assert.NoError(t, err)
		assert.Equal(t, s, string(b[:n]))
	}

	addFrame("abcd")
	shouldRead("abc")
	addFrame("abcd")
	shouldRead("dab")
	shouldRead("cd")
	addFrame("abcd")
	// adding the same frame number again should have no effect
	q.add(&rxFrame{fn: fn, bytes: []byte("1234")}, nil)
	shouldRead("abc")
	shouldRead("d")

	addFrame("abc")
	addFrame("abc")
	go func() {
		time.Sleep(50 * time.Millisecond)
		shouldRead("abc")
	}()
	start := time.Now()
	addFrame("abc")
	assert.InDelta(t, time.Since(start), 50*time.Millisecond, float64(20*time.Millisecond),
		"when receive queue is full, adding frame should wait for available slot")
	shouldRead("abc")
	shouldRead("abc")

	shouldWaitBeforeRead := func(d time.Duration, s string) {
		start := time.Now()
		b := make([]byte, 3)
		n, err := q.read(b)
		assert.NoError(t, err)
		assert.Equal(t, s, string(b[:n]))
		assert.InDelta(t, time.Since(start), d, float64(50*time.Millisecond))
	}
	delay := 100 * time.Millisecond
	time.AfterFunc(delay, func() {
		addFrame("abcd")
	})
	shouldWaitBeforeRead(delay, "abc")
	time.AfterFunc(delay, func() {
		addFrame("abc")
	})
	shouldWaitBeforeRead(0, "d")
	shouldWaitBeforeRead(delay, "abc")

	// frames can be added out of order
	q.add(&rxFrame{fn: fn + 2, bytes: []byte("1234")}, nil)
	time.AfterFunc(delay, func() {
		addFrame("abcd")
	})
	shouldWaitBeforeRead(delay, "abc")
	shouldWaitBeforeRead(0, "d12")
}
