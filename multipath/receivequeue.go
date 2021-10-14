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
	rp                    uint64
	availableFrame        *sync.Cond
	availableFrameChannel chan bool
	readDeadline          time.Time
	deadlineLock          sync.Mutex
	closed                uint32 // 1 == true, 0 == false
}

func newReceiveQueue(size int) *receiveQueue {
	rq := &receiveQueue{
		buf:                   make([]frame, size),
		size:                  uint64(size),
		rp:                    minFrameNumber % uint64(size), // frame number starts with minFrameNumber, so should the read pointer
		availableFrame:        sync.NewCond(&sync.Mutex{}),
		availableFrameChannel: make(chan bool, 1),
	}
	return rq
}

func (rq *receiveQueue) add(f *frame) {
	tries := 0
	for {
		if rq.tryAdd(f) {
			return
		}

		// Protect against the socket being closed
		if atomic.LoadUint32(&rq.closed) == 1 {
			pool.Put(f.bytes)
			return
		}

		if tries < 3 {
			if tries*int(time.Millisecond) > int(time.Millisecond)*50 {
				time.Sleep(time.Millisecond * 50)
			}
			time.Sleep(time.Duration(tries) * time.Millisecond)
		}
		tries++
	}
}

func (rq *receiveQueue) tryAdd(f *frame) bool {
	idx := f.fn % rq.size
	if rq.buf[idx].bytes == nil {
		// empty slot
		rq.buf[idx] = *f
		if idx == rq.rp {
			select {
			case rq.availableFrameChannel <- true:
			default:
			}
		}
		return true
	} else if rq.buf[idx].fn == f.fn {
		// retransmission, ignore
		pool.Put(f.bytes)
		return true
	}
	return false
}

func (rq *receiveQueue) read(b []byte) (int, error) {
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
		<-rq.availableFrameChannel
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
	return totalN, nil
}

func (rq *receiveQueue) setReadDeadline(dl time.Time) {
	rq.deadlineLock.Lock()
	rq.readDeadline = dl
	rq.deadlineLock.Unlock()
	if !dl.IsZero() {
		ttl := dl.Sub(time.Now())
		if ttl <= 0 {
			for {
				abort := false
				select {
				case rq.availableFrameChannel <- true:
				default:
					abort = true
				}
				if abort {
					break
				}
			}
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
	for {
		abort := false
		select {
		case rq.availableFrameChannel <- true:
		default:
			abort = true
		}
		if abort {
			break
		}
	}
}
