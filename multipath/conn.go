package multipath

import (
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type mpConn struct {
	cid        connectionID
	lastFN     uint64
	subflows   []*subflow
	muSubflows sync.RWMutex
	recvQueue  *receiveQueue
	closed     uint32 // 1 == true, 0 == false
}

func newMPConn(cid connectionID) *mpConn {
	return &mpConn{cid: cid,
		lastFN:    minFrameNumber - 1,
		recvQueue: newReceiveQueue(recieveQueueLength),
	}
}
func (bc *mpConn) Read(b []byte) (n int, err error) {
	return bc.recvQueue.read(b)
}

func (bc *mpConn) Write(b []byte) (n int, err error) {
	for _, sf := range bc.sortedSubflows() {
		sf.sendQueue <- composeFrame(atomic.AddUint64(&bc.lastFN, 1), b)
		return len(b), nil
	}
	return 0, ErrClosed
}

func (bc *mpConn) Close() error {
	bc.close()
	for _, sf := range bc.sortedSubflows() {
		sf.close()
	}
	return nil
}

func (bc *mpConn) close() {
	atomic.StoreUint32(&bc.closed, 1)
	bc.recvQueue.close()
}

type fakeAddr struct{}

func (fakeAddr) Network() string { return "multipath" }
func (fakeAddr) String() string  { return "multipath" }

func (bc *mpConn) LocalAddr() net.Addr {
	return fakeAddr{}
}

func (bc *mpConn) RemoteAddr() net.Addr {
	return fakeAddr{}
}

func (bc *mpConn) SetDeadline(t time.Time) error {
	bc.SetReadDeadline(t)
	return bc.SetWriteDeadline(t)
}

func (bc *mpConn) SetReadDeadline(t time.Time) error {
	bc.recvQueue.setReadDeadline(t)
	return nil
}

func (bc *mpConn) SetWriteDeadline(t time.Time) error {
	bc.muSubflows.RLock()
	defer bc.muSubflows.RUnlock()
	for _, sf := range bc.subflows {
		if err := sf.conn.SetWriteDeadline(t); err != nil {
			return err
		}
	}
	return nil
}

func (bc *mpConn) retransmit(frame *sendFrame) {
	frame.retransmissions++
	subflows := bc.sortedSubflows()
	for _, sf := range subflows {
		// choose the first subflow not waiting ack for this frame
		if !sf.isPendingAck(frame.fn) {
			select {
			case <-sf.chClose:
				// continue
			case sf.sendQueue <- frame:
				log.Tracef("retransmitted frame %d via %s", frame.fn, sf.to)
				return
			}
		}
	}
	log.Tracef("frame %d is being retransmitted on all subflows of %x, give up", frame.fn, bc.cid)
	frame.release()
	return
}

func (bc *mpConn) sortedSubflows() []*subflow {
	bc.muSubflows.RLock()
	subflows := make([]*subflow, len(bc.subflows))
	copy(subflows, bc.subflows)
	bc.muSubflows.RUnlock()
	sort.Slice(subflows, func(i, j int) bool {
		return subflows[i].getRTT() < subflows[j].getRTT()
	})
	return subflows
}

func (bc *mpConn) add(to string, c net.Conn, clientSide bool, probeStart time.Time, tracker StatsTracker) {
	bc.muSubflows.Lock()
	defer bc.muSubflows.Unlock()
	bc.subflows = append(bc.subflows, startSubflow(to, c, bc, clientSide, probeStart, tracker))
}

func (bc *mpConn) remove(theSubflow *subflow) {
	bc.muSubflows.Lock()
	var remains []*subflow
	for _, sf := range bc.subflows {
		if sf != theSubflow {
			remains = append(remains, sf)
		}
	}
	bc.subflows = remains
	left := len(remains)
	bc.muSubflows.Unlock()
	if left == 0 {
		bc.close()
	}
}
