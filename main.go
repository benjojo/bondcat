package main

import (
	"context"
	"flag"
	"io"
	"log"
	"math/rand"
	"net"
	"os"
	"time"

	"github.com/getlantern/golog"
	"github.com/getlantern/multipath"
)

func main() {
	verboseFlag := flag.Bool("v", false, "Verbose Mode")
	listenFlag := flag.Bool("l", false, "Listen")
	// debugDontSendZerors := flag.Bool("this-is-not-desktop", false, "dei")
	listenIPsFlag := flag.String("listen-addresses", getListenAddressesDefault(), "")
	// portFlag := flag.Int("p", 0, "Port to listen on")
	flag.Parse()
	otherArgs := flag.Args()
	golog.SetOutputs(os.Stderr, os.Stderr)

	if *listenFlag {
		log.Printf("Listening on %v", *listenIPsFlag)
		l, err := net.Listen("tcp", "0.0.0.0:11231")
		if err != nil {
			log.Fatalf("Can't listen %v", err)
		}
		mpl := multipath.NewListener([]net.Listener{l}, []multipath.StatsTracker{multipath.NullTracker{}})
		if *verboseFlag {
			log.Printf("Listening")
		}
		conn, err := mpl.Accept()
		if err != nil {
			log.Fatalf("Failed to accept bonded connection! %v", err)
		}

		go io.Copy(conn, os.Stdin)
		io.Copy(os.Stdout, conn)
		return
	}

	dialers := make([]multipath.Dialer, 0)
	trackers := make([]multipath.StatsTracker, 0)
	for _, v := range otherArgs {
		dialers = append(dialers, &bondCatDailer{
			dest: v,
		})
		trackers = append(trackers, multipath.NullTracker{})
	}

	d := multipath.NewDialer("", dialers)
	conn, err := d.DialContext(context.Background())
	if err != nil {
		log.Fatalf("failed to dial %v", err)
	}

	DZ := DevZero{
		C: &Counter{},
	}

	DZ.C.RX = 0

	// io.Copy(conn, os.Stdin)
	go io.Copy(os.Stdout, conn)
	// io.Copy(conn, DZ)
	io.Copy(conn, os.Stdin)
	conn.Close()
}

func getListenAddressesDefault() string {
	addresses, err := net.InterfaceAddrs()
	if err != nil {
		log.Printf("Could not auto-probe for available interface addresses (%v)", err)
		return "0.0.0.0"
	}

	if len(addresses) == 0 {
		return "0.0.0.0"
	}

	targets := ""
	for _, v := range addresses {
		if len(targets) == 0 {
			targets = v.String()
		} else {
			targets += "," + v.String()
		}
	}
	return targets
}

type LogTracker struct {
	Prefix string
}

func (st LogTracker) OnRecv(uint64)           {}
func (st LogTracker) OnSent(uint64)           {}
func (st LogTracker) OnRetransmit(uint64)     {}
func (st LogTracker) UpdateRTT(time.Duration) {}

// ben debug

type Counter struct {
	RX, TX uint64
}

type DevZero struct {
	C *Counter
}

func (D DevZero) Write(b []byte) (int, error) {
	before := D.C.RX
	D.C.RX += uint64(len(b))
	if before/1e8 != D.C.RX/1e8 {
		log.Printf("RX'd %d bytes", D.C.RX)
		if before/1e8 == 10 {
			time.Sleep(time.Second * 10)
		}
	}
	return len(b), nil
}

func (D DevZero) Read(b []byte) (int, error) {
	for k := range b {
		b[k] = 0x00
	}

	transmitted := rand.Intn(4096)
	// transmitted := len(b)
	if len(b) < transmitted {
		transmitted = len(b)
	}

	before := D.C.TX
	D.C.TX += uint64(transmitted)
	if before/1e8 != D.C.TX/1e8 {
		log.Printf("TX'd %d bytes", D.C.TX)
	}
	return transmitted, nil
}
