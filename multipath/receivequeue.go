package multipath

import (
	"context"
	"fmt"
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
	availableFrameChannel chan bool
	readDeadline          time.Time
	deadlineLock          sync.Mutex
	closed                uint32 // 1 == true, 0 == false
	readFrameTip          uint64
	readLockmaybeidk      *sync.Mutex
}

func newReceiveQueue(size int) *receiveQueue {
	rq := &receiveQueue{
		buf:                   make([]frame, size),
		size:                  uint64(size),
		rp:                    minFrameNumber % uint64(size), // frame number starts with minFrameNumber, so should the read pointer
		availableFrameChannel: make(chan bool),
		readLockmaybeidk:      &sync.Mutex{},
	}
	return rq
}

func (rq *receiveQueue) add(f *frame, sf *subflow) {

	select {
	case rq.availableFrameChannel <- true:
	default:
	}

	readFrameTip := atomic.LoadUint64(&rq.readFrameTip)

	if readFrameTip != 0 {
		if readFrameTip > f.fn || readFrameTip == f.fn {
			sf.ack(f.fn)
			return
		}
	}

	if f.fn > readFrameTip+rq.size-1 {
		log.Debugf("Near corruption incident?? %v vs the max peek of %v (frametip %d)", f.fn, readFrameTip+rq.size-1, readFrameTip)
		return // Nope! this will corrupt the buffer
	}

	if rq.tryAdd(f) {
		sf.ack(f.fn)
		return
	}

	// Protect against the socket being closed
	if atomic.LoadUint32(&rq.closed) == 1 {
		pool.Put(f.bytes)
		return
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
		log.Debugf("Got a retransmit. for %d", f.fn)
		pool.Put(f.bytes)
		return true
	}

	if idx != 0 {
		log.Debugf("Not what I was looking for, I'm looking for frame %v", rq.buf[idx-1].fn+1)
	}
	return false
}

func (rq *receiveQueue) read(b []byte) (int, error) {
	for {
		if rq.rp != 0 {
			// log.Debugf("looking for %v", rq.buf[rq.rp-1].fn+1)
		}
		if rq.buf[rq.rp].bytes != nil {
			break
		}
		if atomic.LoadUint32(&rq.closed) == 1 {
			return 0, ErrClosed
		}
		if rq.dlExceeded() {
			return 0, context.DeadlineExceeded
		}
		// fmt.Printf("Waiting on <-rq.availableFrameChannel\n")
		<-rq.availableFrameChannel
	}

	rq.readLockmaybeidk.Lock()
	defer rq.readLockmaybeidk.Unlock()

	totalN := 0
	cur := rq.buf[rq.rp].bytes
	for cur != nil && totalN < len(b) {
		oldFrameTip := atomic.LoadUint64(&rq.readFrameTip)
		// fmt.Printf("About to read frame %d\n", rq.buf[rq.rp].fn)
		if (rq.buf[rq.rp].fn != oldFrameTip+1) && (rq.buf[rq.rp].fn != oldFrameTip) && oldFrameTip != 0 {
			log.Errorf("Woops corrupted output, wow I'm dumb %v vs %v (The crash happened at idx = %d)", rq.buf[rq.rp].fn, oldFrameTip+1, rq.rp)
			fmt.Printf("All Buffers: ")
			for idx, v := range rq.buf {
				fmt.Printf("\t[%d]fn %d, [%d]byte\n", idx, v.fn, len(v.bytes))
			}
			rq.close()
			return 0, ErrClosed
		}
		n := copy(b[totalN:], cur)
		if n == len(cur) {
			// fmt.Printf("Finished with read frame %d\n", rq.buf[rq.rp].fn) // BCC DEBUG
			atomic.StoreUint64(&rq.readFrameTip, rq.buf[rq.rp].fn)
			pool.Put(cur)
			rq.buf[rq.rp].bytes = nil
			rq.rp = (rq.rp + 1) % rq.size
		} else {
			// The frames in the ring buffer are never overridden, so we can
			// safely update the bytes to reflect the next read position.
			rq.buf[rq.rp].bytes = cur[n:]
			// fmt.Printf("Partial read frame %d\n", rq.buf[rq.rp].fn) // BCC DEBUG
		}
		totalN += n
		cur = rq.buf[rq.rp].bytes
		rq.buf[rq.rp].bccDebugIveBeenHere = true
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
			time.AfterFunc(ttl, func() {
				rq.availableFrameChannel <- true
			})
		}
	}
}

func (rq *receiveQueue) dlExceeded() bool {
	return !rq.readDeadline.IsZero() && !rq.readDeadline.After(time.Now())
}

func (rq *receiveQueue) close() {
	atomic.StoreUint32(&rq.closed, 1)
	abort := false
	for {
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
