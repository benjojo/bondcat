package multipath

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	pool "github.com/libp2p/go-buffer-pool"
)

// receiveQueue keeps received frames for the upper layer to read. It is
// maintained as a ring buffer with fixed size. It takes advantage of the fact
// that the frame number is sequential, so when a new frame arrives, it is
// placed at buf[frameNumber % size].
type receiveQueue struct {
	buf  []frame
	size uint64
	// rp stands for read pointer, point to the index of the frame containing
	// data yet to be read.
	rp             uint64
	availableFrame *sync.Cond
	availableSlot  *sync.Cond
	readDeadline   time.Time
	closed         uint32 // 1 == true, 0 == false
}

func newReceiveQueue(size int) *receiveQueue {
	rq := &receiveQueue{
		buf:            make([]frame, size),
		size:           uint64(size),
		rp:             minFrameNumber % uint64(size), // frame number starts with minFrameNumber, so should the read pointer
		availableFrame: sync.NewCond(&sync.Mutex{}),
		availableSlot:  sync.NewCond(&sync.Mutex{}),
	}
	return rq
}

func (rq *receiveQueue) add(f *frame) {
	for {
		if rq.tryAdd(f) {
			return
		}
		if !rq.waitForSlot() {
			pool.Put(f.bytes)
			return
		}
	}
}

func (rq *receiveQueue) tryAdd(f *frame) bool {
	idx := f.fn % rq.size
	rq.availableFrame.L.Lock()
	defer rq.availableFrame.L.Unlock()
	if rq.buf[idx].bytes == nil {
		// empty slot
		rq.buf[idx] = *f
		if idx == rq.rp {
			rq.availableFrame.Signal()
		}
		return true
	} else if rq.buf[idx].fn == f.fn {
		// retransmission, ignore
		pool.Put(f.bytes)
		return true
	}
	return false
}

func (rq *receiveQueue) waitForSlot() bool {
	rq.availableSlot.L.Lock()
	rq.availableSlot.Wait()
	rq.availableSlot.L.Unlock()
	if atomic.LoadUint32(&rq.closed) == 1 {
		return false
	}
	return true
}

func (rq *receiveQueue) read(b []byte) (int, error) {
	rq.availableFrame.L.Lock()
	defer rq.availableFrame.L.Unlock()
	for {
		if rq.buf[rq.rp].bytes != nil {
			break
		}
		if atomic.LoadUint32(&rq.closed) == 1 {
			return 0, ErrClosed
		}
		if rq.dlExceeded() {
			return 0, context.DeadlineExceeded
		}
		rq.availableFrame.Wait()
	}
	totalN := 0
	cur := rq.buf[rq.rp].bytes
	for cur != nil && totalN < len(b) {
		n := copy(b[totalN:], cur)
		if n == len(cur) {
			pool.Put(cur)
			rq.buf[rq.rp].bytes = nil
			rq.rp = (rq.rp + 1) % rq.size
		} else {
			// The frames in the ring buffer are never overridden, so we can
			// safely update the bytes to reflect the next read position.
			rq.buf[rq.rp].bytes = cur[n:]
		}
		totalN += n
		cur = rq.buf[rq.rp].bytes
	}
	rq.availableSlot.Signal()
	return totalN, nil
}

func (rq *receiveQueue) setReadDeadline(dl time.Time) {
	rq.availableFrame.L.Lock()
	rq.readDeadline = dl
	rq.availableFrame.L.Unlock()
	if !dl.IsZero() {
		ttl := dl.Sub(time.Now())
		if ttl <= 0 {
			rq.availableFrame.Broadcast()
		} else {
			time.AfterFunc(ttl, rq.availableFrame.Broadcast)
		}
	}
}

func (rq *receiveQueue) dlExceeded() bool {
	return !rq.readDeadline.IsZero() && !rq.readDeadline.After(time.Now())
}

func (rq *receiveQueue) close() {
	atomic.StoreUint32(&rq.closed, 1)
	rq.availableFrame.Broadcast()
	rq.availableSlot.Broadcast()
}
