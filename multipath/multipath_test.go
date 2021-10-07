package multipath

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	_ "net/http/pprof"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

var frozenListeners [100]int
var frozenDailers [100]int
var frozenTrackingLock sync.Mutex

func TestE2E(t *testing.T) {
	// Failing on timeout
	go func() {
		http.ListenAndServe("localhost:6060", nil)
	}()
	listeners := []net.Listener{}
	trackers := []StatsTracker{}
	dialers := []Dialer{}
	for i := 0; i < 3; i++ {
		l, err := net.Listen("tcp", ":")
		if !assert.NoError(t, err) {
			continue
		}
		defer l.Close()
		listeners = append(listeners, newTestListener(l, i))
		trackers = append(trackers, NullTracker{})
		// simulate one or more dialers to each listener
		for j := 0; j <= 1; j++ {
			dialers = append(dialers, newTestDialer(l.Addr().String(), len(dialers)))
		}
	}
	log.Debugf("Testing with %d listeners and %d dialers", len(listeners), len(dialers))
	bl := NewListener(listeners, trackers)
	defer bl.Close()
	bd := NewDialer("endpoint", dialers)

	go func() {
		lastDebug := ""
		for {
			frozenTrackingLock.Lock()
			newDebug := "Dailers: \n"
			for k, v := range dialers {
				newDebug += fmt.Sprintf("\t(%d) - %v\n", frozenDailers[k], v.(*testDialer).name)
			}
			newDebug += "Listeners: \n"
			for k, v := range listeners {
				newDebug += fmt.Sprintf("\t(%d) - %v\n", frozenListeners[k], v.(*testListener).l.Addr())
			}
			if newDebug != lastDebug {
				log.Debug(newDebug)
				lastDebug = newDebug
			}
			frozenTrackingLock.Unlock()
			time.Sleep(time.Millisecond * 33)
		}
	}()

	go func() {
		for {
			conn, err := bl.Accept()
			select {
			case <-bl.(*mpListener).chClose:
				return
			default:
			}
			assert.NoError(t, err)
			go func() {
				defer conn.Close()
				b := make([]byte, 10240)
				for {
					n, err := conn.Read(b)
					if err != nil {
						return
					}
					log.Debugf("server read %d bytes", n)
					n2, err := conn.Write(b[:n])
					if err != nil {
						return
					}
					log.Debugf("server wrote back %d bytes", n2)
				}
			}()
		}
	}()
	conn, err := bd.DialContext(context.Background())
	if !assert.NoError(t, err) {
		return
	}
	defer conn.Close()
	b := make([]byte, 4)
	roundtrip := func() {
		for i := 0; i < 5; i++ {
			copy(b, []byte(strconv.Itoa(i)))
			n, err := conn.Write(b)
			assert.NoError(t, err)
			assert.Equal(t, len(b), n)
			log.Debugf("client written '%s'", b)
			_, err = io.ReadFull(conn, b)
			assert.NoError(t, err)
			log.Debugf("client read '%s'", b)
		}
	}
	roundtrip()

	for i := 0; i < len(listeners)-1; i++ {
		log.Debugf("========listener[%d] is hanging", i)
		frozenTrackingLock.Lock()
		frozenListeners[i] = 1
		frozenTrackingLock.Unlock()
		listeners[i].(*testListener).setDelay(time.Hour)
		roundtrip()
	}
	for i := 0; i < len(dialers)-1; i++ {
		log.Debugf("========%s is hanging", dialers[i].Label())
		frozenTrackingLock.Lock()
		frozenDailers[i] = 1
		frozenTrackingLock.Unlock()
		dialers[i].(*testDialer).setDelay(time.Hour)
		roundtrip()
	}
	log.Debugf("========reenabled listener #0 and %s", dialers[0].Label())
	listeners[0].(*testListener).setDelay(0)
	dialers[0].(*testDialer).setDelay(0)
	frozenTrackingLock.Lock()
	frozenListeners[0] = 0
	frozenDailers[0] = 0
	frozenTrackingLock.Unlock()

	log.Debug("========the last listener is hanging")
	listeners[len(listeners)-1].(*testListener).setDelay(time.Hour)
	frozenTrackingLock.Lock()
	frozenListeners[len(listeners)-1] = 1
	frozenTrackingLock.Unlock()

	roundtrip()
	log.Debugf("========%s is hanging", dialers[len(dialers)-1].Label())
	dialers[len(dialers)-1].(*testDialer).setDelay(time.Hour)
	frozenTrackingLock.Lock()
	frozenDailers[len(dialers)-1] = 1
	frozenTrackingLock.Unlock()
	roundtrip()

	log.Debugf("========Now test writing and reading back tons of data")
	b2 := make([]byte, 81920)
	b3 := make([]byte, 81920)
	rand.Read(b2)
	for i := 0; i < 10; i++ {
		n, err := conn.Write(b2[:rand.Intn(len(b2))])
		assert.NoError(t, err)
		log.Debugf("client wrote %d bytes", n)
		_, err = io.ReadFull(conn, b3[:n])
		assert.NoError(t, err)
		assert.EqualValues(t, b2[:n], b3[:n])
	}

	// wake up all sleeping goroutines to clean up resources
	for i := 0; i < len(listeners); i++ {
		listeners[i].(*testListener).setDelay(0)
	}
	for i := 0; i < len(dialers); i++ {
		dialers[i].(*testDialer).setDelay(0)
	}
}

func TestE2EEarlyClose(t *testing.T) {
	// Reusing the testE2E infra as much as possible
	//
	// this test is here to ensure that mpConns transfer the full
	// set of data transmitted when the connection is closed, to avoid truncation.
	listeners := []net.Listener{}
	trackers := []StatsTracker{}
	dialers := []Dialer{}
	for i := 0; i < 3; i++ {
		l, err := net.Listen("tcp", ":")
		if !assert.NoError(t, err) {
			continue
		}
		defer l.Close()
		listeners = append(listeners, newTestListener(l, i))
		trackers = append(trackers, NullTracker{})
		// simulate one or more dialers to each listener
		for j := 0; j <= 1; j++ {
			dialers = append(dialers, newTestDialer(l.Addr().String(), len(dialers)))
		}
	}
	log.Debugf("Testing with %d listeners and %d dialers", len(listeners), len(dialers))
	bl := NewListener(listeners, trackers)
	defer bl.Close()
	bd := NewDialer("endpoint", dialers)

	go func() {
		lastDebug := ""
		for {
			frozenTrackingLock.Lock()
			newDebug := "Dailers: \n"
			for k, v := range dialers {
				newDebug += fmt.Sprintf("\t(%d) - %v\n", frozenDailers[k], v.(*testDialer).name)
			}
			newDebug += "Listeners: \n"
			for k, v := range listeners {
				newDebug += fmt.Sprintf("\t(%d) - %v\n", frozenListeners[k], v.(*testListener).l.Addr())
			}
			if newDebug != lastDebug {
				log.Debug(newDebug)
				lastDebug = newDebug
			}
			frozenTrackingLock.Unlock()
			time.Sleep(time.Millisecond * 33)
		}
	}()

	go func() {
		for {
			conn, err := bl.Accept()
			select {
			case <-bl.(*mpListener).chClose:
				return
			default:
			}
			assert.NoError(t, err)
			go func() {
				defer conn.Close()
				dataLeftToSend := 10 * 100000000 // 10MB
				b := make([]byte, 10240)
				for {
					var n int
					var err error
					if dataLeftToSend < len(b) {
						n, err = conn.Write(b[:dataLeftToSend])
					} else {
						n, err = conn.Write(b)
					}
					if err != nil {
						return
					}
					dataLeftToSend = dataLeftToSend - n

					if dataLeftToSend == 0 {
						return
					}
				}
			}()
		}
	}()
	conn, err := bd.DialContext(context.Background())
	if !assert.NoError(t, err) {
		return
	}
	defer conn.Close()

	readBytes := 0
	for {
		b := make([]byte, 1024)
		n, err := conn.Read(b)
		if err != nil {
			fmt.Printf("Connection closed early at %v (%v)\n", readBytes, err)
			t.FailNow()
		}
		readBytes += n
		if readBytes == 10*100000000 {
			// pass!
			break
		}
	}
	t.Fatalf("aaa %v", readBytes)
}

func TestE2EEarlyCloseOtherWay(t *testing.T) {
	// Reusing the testE2E infra as much as possible
	//
	// this test is here to ensure that mpConns transfer the full
	// set of data transmitted when the connection is closed, to avoid truncation.
	listeners := []net.Listener{}
	trackers := []StatsTracker{}
	dialers := []Dialer{}
	for i := 0; i < 3; i++ {
		l, err := net.Listen("tcp", ":")
		if !assert.NoError(t, err) {
			continue
		}
		defer l.Close()
		listeners = append(listeners, newTestListener(l, i))
		trackers = append(trackers, NullTracker{})
		// simulate one or more dialers to each listener
		for j := 0; j <= 1; j++ {
			dialers = append(dialers, newTestDialer(l.Addr().String(), len(dialers)))
		}
	}
	log.Debugf("Testing with %d listeners and %d dialers", len(listeners), len(dialers))
	bl := NewListener(listeners, trackers)
	defer bl.Close()
	bd := NewDialer("endpoint", dialers)

	go func() {
		lastDebug := ""
		for {
			frozenTrackingLock.Lock()
			newDebug := "Dailers: \n"
			for k, v := range dialers {
				newDebug += fmt.Sprintf("\t(%d) - %v\n", frozenDailers[k], v.(*testDialer).name)
			}
			newDebug += "Listeners: \n"
			for k, v := range listeners {
				newDebug += fmt.Sprintf("\t(%d) - %v\n", frozenListeners[k], v.(*testListener).l.Addr())
			}
			if newDebug != lastDebug {
				log.Debug(newDebug)
				lastDebug = newDebug
			}
			frozenTrackingLock.Unlock()
			time.Sleep(time.Millisecond * 33)
		}
	}()

	go func() {
		for {
			conn, err := bl.Accept()
			select {
			case <-bl.(*mpListener).chClose:
				return
			default:
			}
			assert.NoError(t, err)
			go func() {
				defer conn.Close()

				readBytes := 0
				for {
					b := make([]byte, 1024)
					n, err := conn.Read(b)
					if err != nil {
						fmt.Printf("Connection closed early at %v (%v)\n", readBytes, err)
						t.FailNow()
					}
					readBytes += n
					if readBytes == 10*100000000 {
						// pass!
						return
					}
				}
			}()
		}
	}()
	conn, err := bd.DialContext(context.Background())
	if !assert.NoError(t, err) {
		return
	}

	defer conn.Close()
	dataLeftToSend := 10 * 100000000 // 10MB
	b := make([]byte, 10210)
	for {
		var n int
		var err error
		if dataLeftToSend < len(b) {
			n, err = conn.Write(b[:dataLeftToSend])
		} else {
			n, err = conn.Write(b)
		}
		if err != nil {
			t.Fatalf("Failed to write %v", err)
		}
		dataLeftToSend = dataLeftToSend - n

		if dataLeftToSend == 0 {
			return
		}
	}

}

type testDialer struct {
	delayEnforcer
	addr string
	idx  int
}

func newTestDialer(addr string, idx int) *testDialer {
	var lock sync.Mutex
	td := &testDialer{
		delayEnforcer{cond: sync.NewCond(&lock)}, addr, idx,
	}
	td.delayEnforcer.name = td.Label()
	return td
}

func (td *testDialer) DialContext(ctx context.Context) (net.Conn, error) {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "tcp", td.addr)
	if err != nil {
		return nil, err
	}
	return &laggedConn{conn, conn, td.delayEnforcer.sleep}, nil
}

func (td *testDialer) Label() string {
	return fmt.Sprintf("test dialer #%d to %v", td.idx, td.addr)
}

type testListener struct {
	net.Listener
	delayEnforcer
	l net.Listener
}

func newTestListener(l net.Listener, idx int) *testListener {
	var lock sync.Mutex
	tl := &testListener{l, delayEnforcer{cond: sync.NewCond(&lock)}, l}
	tl.delayEnforcer.name = fmt.Sprintf("listener %d", idx)
	return tl
}

func (tl *testListener) Accept() (net.Conn, error) {
	conn, err := tl.l.Accept()
	if err != nil {
		return nil, err
	}
	return &laggedConn{conn, conn, tl.delayEnforcer.sleep}, nil
}

type laggedConn struct {
	net.Conn
	conn  net.Conn // has to be the same as the net.Conn
	sleep func()
}

func (c *laggedConn) Read(b []byte) (int, error) {
	c.sleep()
	return c.conn.Read(b)
}

func TestDelayEnforcer(t *testing.T) {
	var lock sync.Mutex
	d := delayEnforcer{cond: sync.NewCond(&lock)}
	var wg sync.WaitGroup
	d.setDelay(time.Hour)
	wg.Add(1)
	start := time.Now()
	go func() {
		d.sleep()
		wg.Done()
	}()
	time.Sleep(100 * time.Millisecond)
	d.setDelay(0)
	wg.Wait()
	assert.InDelta(t, time.Since(start), 100*time.Millisecond, float64(10*time.Millisecond))
}

type delayEnforcer struct {
	name  string
	delay int64
	cond  *sync.Cond
}

func (e *delayEnforcer) setDelay(d time.Duration) {
	atomic.StoreInt64(&e.delay, int64(d))
	log.Debugf("%s delay is set to %v", e.name, d)
	e.cond.Broadcast()
}

func (e *delayEnforcer) sleep() {
	e.cond.L.Lock()
	defer e.cond.L.Unlock()
	for {
		d := atomic.LoadInt64(&e.delay)
		if delay := time.Duration(d); delay > 0 {
			log.Debugf("%s sleep for %v", e.name, delay)
			time.AfterFunc(delay, func() {
				e.cond.Broadcast()
			})
			e.cond.Wait()
			log.Debugf("%s done sleeping", e.name)
		} else {
			return
		}
	}
}
