package multipath

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net"
	"sort"
	"sync/atomic"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/getlantern/ema"
)

// Dialer is the interface each subflow dialer needs to satisify. It is also
// the type of the multipath dialer.
type Dialer interface {
	DialContext(ctx context.Context) (net.Conn, error)
	Label() string
}

// Stats is also provided by the multipath dialer so the caller can get and
// print the status of each path.
type Stats interface {
	FormatStats() (stats []string)
}

type subflowDialer struct {
	Dialer
	label            string
	successes        uint64
	consecSuccesses  uint64
	failures         uint64
	framesSent       uint64
	framesRetransmit uint64
	framesRecv       uint64
	bytesSent        uint64
	bytesRetransmit  uint64
	bytesRecv        uint64
	emaRTT           *ema.EMA
}

func (sfd *subflowDialer) DialContext(ctx context.Context) (net.Conn, error) {
	conn, err := sfd.Dialer.DialContext(ctx)
	if err == nil {
		atomic.AddUint64(&sfd.successes, 1)
		atomic.AddUint64(&sfd.consecSuccesses, 1)
	} else {
		// reset RTT to deprioritize this dialer
		sfd.emaRTT.SetDuration(longRTT)
		atomic.AddUint64(&sfd.failures, 1)
		atomic.StoreUint64(&sfd.consecSuccesses, 0)
	}
	return conn, err
}

func (sfd *subflowDialer) OnRecv(n uint64) {
	atomic.AddUint64(&sfd.framesRecv, 1)
	atomic.AddUint64(&sfd.bytesRecv, n)
}
func (sfd *subflowDialer) OnSent(n uint64) {
	atomic.AddUint64(&sfd.framesSent, 1)
	atomic.AddUint64(&sfd.bytesSent, n)
}
func (sfd *subflowDialer) OnRetransmit(n uint64) {
	atomic.AddUint64(&sfd.framesRetransmit, 1)
	atomic.AddUint64(&sfd.bytesRetransmit, n)
}
func (sfd *subflowDialer) UpdateRTT(rtt time.Duration) {
	sfd.emaRTT.UpdateDuration(rtt)
}

type mpDialer struct {
	dest    string
	dialers []*subflowDialer
}

func NewDialer(dest string, dialers []Dialer) Dialer {
	var subflowDialers []*subflowDialer
	for _, d := range dialers {
		subflowDialers = append(subflowDialers, &subflowDialer{Dialer: d, label: d.Label(), emaRTT: ema.NewDuration(longRTT, rttAlpha)})
	}
	d := &mpDialer{dest, subflowDialers}
	return d
}

func (mpd *mpDialer) dialOne(d *subflowDialer, cid connectionID, bc *mpConn, ctx context.Context) (connectionID, bool, *mpConn) {
	conn, err := d.DialContext(ctx)
	if err != nil {
		log.Errorf("failed to dial %s: %v", d.Label(), err)
		return zeroCID, false, bc
	}
	probeStart := time.Now()
	newCID, err := mpd.handshake(conn, cid)
	if err != nil {
		log.Errorf("failed to handshake %s, continuing: %v", d.Label(), err)
		conn.Close()
		return zeroCID, false, bc
	}
	if cid == zeroCID {
		bc = newMPConn(newCID)
		go func() {
			for {
				time.Sleep(time.Second)
				bc.pendingAckMu.RLock()
				oldest := time.Duration(0)
				oldestFN := uint64(0)
				for fn, frame := range bc.pendingAckMap {
					if time.Since(frame.sentAt) > oldest {
						oldest = time.Since(frame.sentAt)
						oldestFN = fn
					}
				}
				bc.pendingAckMu.RUnlock()
				if oldest > time.Second {
					log.Debugf("Frame %d has not been acked for %v\n", oldestFN, oldest)
				}
			}
		}()
	}
	bc.add(fmt.Sprintf("%x(%s)", newCID, d.label), conn, true, probeStart, d)
	return newCID, true, bc
}

// DialContext dials the addr using all dialers and returns a connection
// contains subflows from whatever dialers available.
func (mpd *mpDialer) DialContext(ctx context.Context) (net.Conn, error) {
	var bc *mpConn
	dialers := mpd.sorted()
	for i, d := range dialers {
		// dial the first connection with zero connection ID
		dialctx, dialcancel := context.WithTimeout(ctx, time.Second*2)
		defer dialcancel()
		cid, ok, bc := mpd.dialOne(d, zeroCID, bc, dialctx)
		if !ok {
			continue
		}
		if i < len(dialers)-1 {
			// dial the rest in parallel with server assigned connection ID
			for _, d := range dialers[i+1:] {
				go mpd.dialOne(d, cid, bc, ctx)
			}
		}
		return bc, nil
	}
	return nil, ErrFailOnAllDialers
}

// handshake exchanges version and cid with the peer and returns the connnection ID
// both end agrees if no error happens.
func (mpd *mpDialer) handshake(conn net.Conn, cid connectionID) (connectionID, error) {
	var leadBytes [leadBytesLength]byte
	// the first byte, version, is implicitly set to 0
	copy(leadBytes[1:], cid[:])
	_, err := conn.Write(leadBytes[:])
	if err != nil {
		return zeroCID, err
	}
	_, err = io.ReadFull(conn, leadBytes[:])
	if err != nil {
		return zeroCID, err
	}
	if uint8(leadBytes[0]) != 0 {
		return zeroCID, ErrUnexpectedVersion
	}
	var newCID connectionID
	copy(newCID[:], leadBytes[1:])
	if cid != zeroCID && cid != newCID {
		return zeroCID, ErrUnexpectedCID
	}
	return newCID, nil
}

func (mpd *mpDialer) Label() string {
	return fmt.Sprintf("multipath dialer to %s with %d paths", mpd.dest, len(mpd.dialers))
}

func (mpd *mpDialer) sorted() []*subflowDialer {
	dialersCopy := make([]*subflowDialer, len(mpd.dialers))
	copy(dialersCopy, mpd.dialers)
	sort.Slice(dialersCopy, func(i, j int) bool {
		it := dialersCopy[i].emaRTT.GetDuration()
		jt := dialersCopy[j].emaRTT.GetDuration()
		// both have unknown RTT or fail to dial, give each a chance
		if it == jt {
			return rand.Intn(2) > 0
		}
		return it < jt
	})
	return dialersCopy
}

func (mpd *mpDialer) FormatStats() (stats []string) {
	for _, d := range mpd.sorted() {
		stats = append(stats, fmt.Sprintf("%s  S: %4d(%3d)  F: %4d  RTT: %6.0fms  SENT: %7d/%7s  RECV: %7d/%7s  RT: %7d/%7s",
			d.label,
			atomic.LoadUint64(&d.successes),
			atomic.LoadUint64(&d.consecSuccesses),
			atomic.LoadUint64(&d.failures),
			d.emaRTT.GetDuration().Seconds()*1000,
			atomic.LoadUint64(&d.framesSent), humanize.Bytes(atomic.LoadUint64(&d.bytesSent)),
			atomic.LoadUint64(&d.framesRecv), humanize.Bytes(atomic.LoadUint64(&d.bytesRecv)),
			atomic.LoadUint64(&d.framesRetransmit), humanize.Bytes(atomic.LoadUint64(&d.bytesRetransmit))))
	}
	return
}
