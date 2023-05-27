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
	readFrameTip uint64
	buf          []rxFrame
	size         uint64
	// rp stands for read pointer, point to the index of the frame containing
	// data yet to be read.
	rp                    uint64
	availableFrameChannel chan bool
	readNotifyChannel     chan bool
	readDeadline          time.Time
	deadlineLock          sync.Mutex
	closing               uint32 // 1 == true, 0 == false  -- This is used to "drain" the Queue
	fullyClosed           uint32 // 1 == true, 0 == false
	readLock              *sync.Mutex
}

func newReceiveQueue(size int) *receiveQueue {
	rq := &receiveQueue{
		buf:                   make([]rxFrame, size),
		size:                  uint64(size),
		rp:                    minFrameNumber % uint64(size), // frame number starts with minFrameNumber, so should the read pointer
		availableFrameChannel: make(chan bool, 1),
		readNotifyChannel:     make(chan bool),
		readLock:              &sync.Mutex{},
	}
	return rq
}

func (rq *receiveQueue) add(f *rxFrame, sf *subflow) {
	select {
	case rq.availableFrameChannel <- true:
	default:
	}
	// Another thing to protect against, is that we might be
	// locally blocked on a full receiveQueue. If that is the
	// case then we don't want to return instantly from this
	// function since that will just case retransmits to fire
	// over and over again, causing mass bandwidth loss.
	// Instead let's quickly check if we have all of the data we need
	// to read, and if we do, hang until we don't have that problem anymore
	if rq.isFull() {
		for {
			var abort bool
			select {
			case <-rq.readNotifyChannel:
				if !rq.isFull() {
					abort = true
					break
				}
			}
			if abort {
				break
			}
		}
	}
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

	if f.fn > readFrameTip+rq.size && readFrameTip != 0 {
		log.Debugf("Near corruption incident?? %v vs the max peek of %v (frametip %d)", f.fn, readFrameTip+rq.size-1, readFrameTip)
		return // Nope! this will corrupt the buffer
	}

	if rq.tryAdd(f) {
		sf.ack(f.fn)
		return
	}

	// Protect against the socket being closed
	if atomic.LoadUint32(&rq.fullyClosed) == 1 {
		pool.Put(f.bytes)
		return
	}

}

func (rq *receiveQueue) isFull() bool {
	printFull := false
	for i := uint64(0); i < rq.size; i++ {
		expectedFrameNumber := atomic.LoadUint64(&rq.readFrameTip) + i
		idx := expectedFrameNumber % rq.size

		rq.readLock.Lock()
		if rq.buf[idx].fn != expectedFrameNumber {
			if printFull {
				log.Tracef("receiveQueue is %d%% full! (%d/%d)", int((float32(i) / float32(rq.size) * 100)), i, rq.size)
			}
			rq.readLock.Unlock()
			return false
		}

		if rq.buf[idx].bytes == nil {
			rq.readLock.Unlock()
			return false
		}
		rq.readLock.Unlock()

		if i == rq.size/2 {
			printFull = true
		}
	}

	return true
}

func (rq *receiveQueue) tryAdd(f *rxFrame) bool {
	rq.readLock.Lock()
	idx := f.fn % rq.size
	if rq.buf[idx].fn == f.fn {
		rq.readLock.Unlock()
		// retransmission, ignore
		log.Tracef("Got a retransmit. for %d", f.fn)
		pool.Put(f.bytes)
		return true
	} else if rq.buf[idx].bytes == nil {
		// empty slot
		rq.buf[idx] = *f
		if idx == rq.rp {
			select {
			case rq.availableFrameChannel <- true:
			default:
			}
		}
		rq.readLock.Unlock()
		return true
	}
	rq.readLock.Unlock()

	if idx != 0 {
		log.Tracef("Not what I was looking for, I'm looking for frame %v", rq.buf[idx-1].fn+1)
	}
	return false
}

func (rq *receiveQueue) read(b []byte) (int, error) {
	for {
		rq.readLock.Lock()
		if rq.buf[rq.rp].bytes != nil {
			rq.readLock.Unlock()
			break
		}
		rq.readLock.Unlock()

		if atomic.LoadUint32(&rq.fullyClosed) == 1 {
			return 0, ErrClosed
		}
		if atomic.LoadUint32(&rq.closing) == 1 {
			// if we are closing, then we should check if there is anything left to send
			// before sending ErrClosed back upstream, otherwise we may close "early" with
			// some data still inside of us!
			break
		}

		if rq.dlExceeded() {
			return 0, context.DeadlineExceeded
		}

		select {
		case rq.readNotifyChannel <- true:
		default:
		}
		<-rq.availableFrameChannel
	}

	rq.readLock.Lock()
	defer rq.readLock.Unlock()

	totalN := 0
	cur := rq.buf[rq.rp].bytes
	for cur != nil && totalN < len(b) {
		oldFrameTip := atomic.LoadUint64(&rq.readFrameTip)
		if (rq.buf[rq.rp].fn != oldFrameTip+1) && (rq.buf[rq.rp].fn != oldFrameTip) && oldFrameTip != 0 {
			log.Errorf("receiveQueue buffer corruption detected [%v vs %v] (The crash happened at idx = %d)", rq.buf[rq.rp].fn, oldFrameTip+1, rq.rp)
			log.Tracef("All Buffers: ")
			for idx, v := range rq.buf {
				log.Tracef("\t[%d]fn %d, [%d]byte\n", idx, v.fn, len(v.bytes))
			}
			rq.close()
			return 0, ErrClosed
		}
		n := copy(b[totalN:], cur)
		if n == len(cur) {
			log.Tracef("Finished with read frame %d\n", rq.buf[rq.rp].fn)
			atomic.StoreUint64(&rq.readFrameTip, rq.buf[rq.rp].fn)
			pool.Put(cur)
			rq.buf[rq.rp].bytes = nil
			rq.rp = (rq.rp + 1) % rq.size
		} else {
			// The frames in the ring buffer are never overridden, so we can
			// safely update the bytes to reflect the next read position.
			rq.buf[rq.rp].bytes = cur[n:]
			log.Tracef("Partial read frame %d\n", rq.buf[rq.rp].fn)
		}
		totalN += n
		cur = rq.buf[rq.rp].bytes
	}

	select {
	case rq.readNotifyChannel <- true:
	default:
	}

	if totalN == 0 && atomic.LoadUint32(&rq.closing) == 1 {
		// close fully
		atomic.StoreUint32(&rq.fullyClosed, 1)
		return 0, ErrClosed
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
	atomic.StoreUint32(&rq.closing, 1)
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
