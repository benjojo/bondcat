package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"time"

	"github.com/getlantern/multipath"
)

func main() {
	verboseFlag := flag.Bool("v", false, "Verbose Mode")
	listenFlag := flag.Bool("l", false, "Listen")
	listenIPsFlag := flag.String("listen-addresses", getListenAddressesDefault(), "")
	// portFlag := flag.Int("p", 0, "Port to listen on")
	flag.Parse()
	otherArgs := flag.Args()
	fmt.Printf(".")

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

	go io.Copy(conn, os.Stdin)
	io.Copy(os.Stdout, conn)
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
