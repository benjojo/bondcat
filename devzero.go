package main

import (
	"fmt"
	"os"
	"sync"
	"time"
)

func makeDevZero() DevZero {
	c := Counter{}
	return DevZero{&c, &sync.Mutex{}}
}

type Counter struct {
	RX, TX uint64
}

type DevZero struct {
	C *Counter
	L *sync.Mutex
}

func (D DevZero) Monitor() {
	oldRX, oldTX := uint64(0), uint64(0)
	for {
		D.L.Lock()
		fmt.Fprintf(os.Stderr, "RX: %v (%v/s)\t\tTX: %v (%v/s)\n",
			ByteCountIEC(int64(D.C.RX)), ByteCountIEC(int64(D.C.RX-oldRX)),
			ByteCountIEC(int64(D.C.TX)), ByteCountIEC(int64(D.C.TX-oldTX)))
		oldRX, oldTX = D.C.RX, D.C.TX
		D.L.Unlock()
		time.Sleep(time.Second)
	}
}

func (D DevZero) Close() error {
	return nil
}

func (D DevZero) Write(b []byte) (int, error) {
	D.L.Lock()
	defer D.L.Unlock()

	D.C.RX += uint64(len(b))
	return len(b), nil
}

func (D DevZero) Read(b []byte) (int, error) {
	D.L.Lock()
	defer D.L.Unlock()

	for k := range b {
		b[k] = 0x00
	}

	transmitted := len(b)
	if len(b) < transmitted {
		transmitted = len(b)
	}

	D.C.TX += uint64(transmitted)
	return transmitted, nil
}

func ByteCountIEC(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB",
		float64(b)/float64(div), "KMGTPE"[exp])
}
