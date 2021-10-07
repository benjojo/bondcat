package multipath

import (
	"net"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

type mpConn struct {
	cid              connectionID
	lastFN           uint64
	subflows         []*subflow
	muSubflows       sync.RWMutex
	recvQueue        *receiveQueue
	closed           uint32 // 1 == true, 0 == false
	writerMaybeReady chan bool
	tryRetransmit    chan bool

	pendingAckMap map[uint64]*pendingAck
	pendingAckMu  *sync.RWMutex
}

func newMPConn(cid connectionID) *mpConn {
	mpc := &mpConn{cid: cid,
		lastFN:           minFrameNumber - 1,
		recvQueue:        newReceiveQueue(recieveQueueLength),
		writerMaybeReady: make(chan bool, 1),
		tryRetransmit:    make(chan bool, 1),
		pendingAckMap:    make(map[uint64]*pendingAck),
		pendingAckMu:     &sync.RWMutex{},
	}
	go mpc.retransmitLoop()
	return mpc
}
func (bc *mpConn) Read(b []byte) (n int, err error) {
	return bc.recvQueue.read(b)
}

func (bc *mpConn) Write(b []byte) (n int, err error) {
	frame := composeFrame(atomic.AddUint64(&bc.lastFN, 1), b)

	for {
		bc.pendingAckMu.RLock()
		inflight := len(bc.pendingAckMap)
		bc.pendingAckMu.RUnlock()
		if inflight > 500 {
			time.Sleep(time.Millisecond * 100)
			log.Tracef("too many inflights")
			continue
		}

		for _, sf := range bc.sortedSubflows() {

			if atomic.LoadUint64(&sf.actuallyBusyOnWrite) == 1 {
				// Avoid a possibly blocked writer for a retransmit
				continue
			}

			select {
			case sf.sendQueue <- frame:
				return len(b), nil
			default:
			}
		}
		if len(bc.sortedSubflows()) == 0 {
			return 0, ErrClosed
		}

		<-bc.writerMaybeReady
	}
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
	frame.changeLock.Lock()
	defer frame.changeLock.Unlock()

	if atomic.LoadUint64(&frame.beingRetransmitted) == 1 {
		return
	}
	atomic.StoreUint64(&frame.beingRetransmitted, 1)
	defer func() {
		atomic.StoreUint64(&frame.beingRetransmitted, 0)
	}()

	subflows := bc.sortedSubflows()

	alreadyTransmittedOnAllSubflows := false
	for {
		abort := false
		if bc.closed == 1 {
			return
		}

		var selectedSubflow *subflow

		abort, alreadyTransmittedOnAllSubflows, selectedSubflow = selectSubflowForRetransmit(subflows, frame, false)
		if selectedSubflow == nil {
			abort, alreadyTransmittedOnAllSubflows, selectedSubflow = selectSubflowForRetransmit(subflows, frame, true)
			if selectedSubflow == nil {
				abort = true
				alreadyTransmittedOnAllSubflows = true
				break
			}
		}

		select {
		case <-selectedSubflow.chClose:
			continue
		case selectedSubflow.sendQueue <- frame:
			frame.retransmissions++
			log.Debugf("retransmitted frame %d via %s", frame.fn, selectedSubflow.to)
			if frame.sentVia == nil {
				frame.sentVia = make([]transmissionDatapoint, 0)
			}
			frame.sentVia = append(frame.sentVia, transmissionDatapoint{selectedSubflow, time.Now()})
			return
		default:
		}

		if abort {
			break
		}
		<-bc.tryRetransmit
	}

	if !alreadyTransmittedOnAllSubflows {
		log.Debugf("frame %d is being retransmitted on all subflows of %x", frame.fn, bc.cid)
	}

	return
}

func selectSubflowForRetransmit(subflows []*subflow, frame *sendFrame, timeFallback bool) (bool, bool, *subflow) {
	var selectedSubflow *subflow
	for _, sf := range subflows {
		if atomic.LoadUint64(&sf.actuallyBusyOnWrite) == 1 {
			// Avoid a possibly blocked writer for a retransmit
			// let's avoid re-sending a frame down the same socket twice.
			// Since at best, it just double sends a frame into the send buffer
			// and at worst it blocks other frames from entering a send buffer.
			continue
		}
		// Have we used this subflow before for this frame?
		usedBefore := false
		var avoidTime time.Time
		for _, avoidSF := range frame.sentVia {
			if sf == avoidSF.sf {
				usedBefore = true
				avoidTime = avoidSF.txTime
			}
		}

		// It may be acceptable to use a subflow that has been used before
		// if we are in timeFallback mode
		if usedBefore {
			if timeFallback {
				if time.Since(avoidTime) > time.Second {
					usedBefore = false
				}
			} else {
				continue
			}
		}

		if !usedBefore {
			// frame.sentVia
			return false, false, sf
		}
	}
	return true, true, selectedSubflow
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

func (bc *mpConn) retransmitLoop() {
	evalTick := time.NewTicker(time.Millisecond * 100)
	for {
		select {
		case <-evalTick.C:
		}
		if atomic.LoadUint32(&bc.closed) == 1 {
			return
		}

		bc.pendingAckMu.RLock()
		RetransmitFrames := make([]pendingAck, 0)
		for fn, frame := range bc.pendingAckMap {
			if time.Since(frame.sentAt) > frame.outboundSf.retransTimer() {
				if bc.pendingAckMap[fn] != nil {
					RetransmitFrames = append(RetransmitFrames, *frame)
				}
			}
		}
		bc.pendingAckMu.RUnlock()

		sort.Slice(RetransmitFrames, func(i, j int) bool {
			return RetransmitFrames[i].fn < RetransmitFrames[j].fn
		})

		for _, frame := range RetransmitFrames {
			sendframe := frame.framePtr
			sendframe.changeLock.Lock()
			if bc.isPendingAck(frame.fn) {
				// No ack means the subflow fails or has a longer RTT
				// log.Errorf("Retransmitting! %#v", frame.fn)
				if sendframe.beingRetransmitted == 0 {
					go bc.retransmit(sendframe)
				}
				sendframe.changeLock.Unlock()
			} else {
				// It is ok to release buffer here as the frame will never
				// be retransmitted again.
				sendframe.release()
				sendframe.changeLock.Unlock()
				bc.pendingAckMu.Lock()
				delete(bc.pendingAckMap, frame.fn)
				bc.pendingAckMu.Unlock()
			}
		}

	}
}

func (bc *mpConn) isPendingAck(fn uint64) bool {
	if fn > minFrameNumber {
		bc.pendingAckMu.RLock()
		defer bc.pendingAckMu.RUnlock()
		return bc.pendingAckMap[fn] != nil
	}
	return false

}
