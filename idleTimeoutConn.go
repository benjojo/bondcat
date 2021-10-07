package main

import (
	"net"
	"sync"
	"time"
)

type idleTimeoutConn struct {
	timeoutValue time.Duration
	resetTimer   chan bool
	*sync.Once
	net.Conn
}

func (i idleTimeoutConn) waitForTimeout() {
	resetSignal := time.NewTimer(i.timeoutValue)
	for {
		select {
		case <-i.resetTimer:
			resetSignal.Reset(i.timeoutValue)
		case <-resetSignal.C:
			i.Close()
		}
	}
}

func (i idleTimeoutConn) Read(b []byte) (n int, err error) {
	i.Do(func() {
		go func() {
			i.waitForTimeout()
		}()
	})
	i.resetTimer <- true
	return i.Conn.Read(b)
}

func (i idleTimeoutConn) Write(b []byte) (n int, err error) {
	i.Do(func() {
		go func() {
			i.waitForTimeout()
		}()
	})
	i.resetTimer <- true
	return i.Conn.Write(b)
}

func (i idleTimeoutConn) Close() error {
	return i.Conn.Close()
}

func (i idleTimeoutConn) LocalAddr() net.Addr {
	return i.Conn.LocalAddr()
}

func (i idleTimeoutConn) RemoteAddr() net.Addr {
	return i.Conn.RemoteAddr()
}

func (i idleTimeoutConn) SetDeadline(t time.Time) error {
	return i.Conn.SetDeadline(t)
}

func (i idleTimeoutConn) SetReadDeadline(t time.Time) error {
	return i.Conn.SetReadDeadline(t)
}

func (i idleTimeoutConn) SetWriteDeadline(t time.Time) error {
	return i.Conn.SetWriteDeadline(t)
}
