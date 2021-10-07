package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"sync"
	"time"

	"github.com/getlantern/golog"
	"github.com/getlantern/multipath"
)

var (
	verboseFlag bool
)

func main() {
	var (
		listenFlag, noAutoDetect        bool
		ipv4OnlyFlag, ipv6OnlyFlag      bool
		benchmarkMode                   bool
		idleTimeoutFlag, listenPortFlag int
		multiplierFlag                  int
		relayHostFlag, execBinFlag      string
	)

	initHelp()
	flag.BoolVar(&noAutoDetect, "a", false, "Disable bind auto detection")
	flag.BoolVar(&noAutoDetect, "no-auto-detect", false, "Disable bind auto detection")

	flag.BoolVar(&verboseFlag, "v", false, "Verbose mode")
	flag.BoolVar(&verboseFlag, "verbose", false, "Verbose mode")

	flag.BoolVar(&listenFlag, "l", false, "Listen for connections")
	flag.BoolVar(&listenFlag, "listen", false, "Listen for connections")
	flag.IntVar(&listenPortFlag, "p", -1, "What port to listen on in listen mode")

	flag.BoolVar(&ipv6OnlyFlag, "6", false, "Only use IPv6")
	flag.BoolVar(&ipv4OnlyFlag, "4", false, "Only use IPv4")

	flag.IntVar(&idleTimeoutFlag, "i", 0, "Close connection after (n) seconds of inactivity")
	flag.IntVar(&idleTimeoutFlag, "idle-timeout", 0, "Close connection after (n) seconds of inactivity")

	flag.StringVar(&relayHostFlag, "relay", "", "Proxy inbound connections to host:port, can only be used with the listen option")
	flag.StringVar(&execBinFlag, "e", "", "Run program instead of connecting to stdin/stdout , can only be used with the listen option")
	flag.StringVar(&execBinFlag, "exec", "", "Run program instead of connecting to stdin/stdout, can only be used with the listen option")

	flag.BoolVar(&benchmarkMode, "b", false, "Benchmark mode (Will tx and rx at full speed)")
	flag.BoolVar(&benchmarkMode, "benchmark", false, "Benchmark mode (Will tx and rx at full speed)")

	flag.IntVar(&multiplierFlag, "x", 0, "How many sub-connections to start for each endpoint")
	flag.IntVar(&multiplierFlag, "multiplier", 0, "How many sub-connections to start for each endpoint")
	flag.Parse()

	if multiplierFlag == 0 {
		multiplierFlag = 1
	}

	otherArgs := flag.Args()
	if verboseFlag {
		golog.SetOutputs(os.Stderr, os.Stderr)
	} else {
		golog.SetOutputs(makeDevZero(), makeDevZero())
	}

	var mpConn net.Conn

	if listenFlag {
		mpConn = buildListenMpConn(listenPortFlag, noAutoDetect, ipv6OnlyFlag, ipv4OnlyFlag)
	} else {
		mpConn = buildDialMpConn(otherArgs, ipv6OnlyFlag, ipv4OnlyFlag, multiplierFlag, noAutoDetect)
		if mpConn == nil {
			flag.Usage()
			return
		}
	}

	if mpConn == nil {
		fmt.Printf("Failed to build a multipath connection. Either all of your endpoints are down, or they cannot be resolved.\n")
		return
	}

	defer mpConn.Close()
	closeCh := make(chan bool)

	if idleTimeoutFlag != 0 {
		mpConn = idleTimeoutConn{
			timeoutValue: time.Second * time.Duration(idleTimeoutFlag),
			resetTimer:   make(chan bool),
			Once:         &sync.Once{},
			Conn:         mpConn,
		}
	}

	if benchmarkMode {
		dz := makeDevZero()
		go func() {
			io.Copy(dz, mpConn)
			closeCh <- true
		}()
		go func() {
			io.Copy(mpConn, dz)
			closeCh <- true
		}()
		go dz.Monitor()
		<-closeCh
		return
	}

	if relayHostFlag != "" {
		relayConn, err := net.Dial("tcp", relayHostFlag)
		if err != nil {
			log.Printf("Cannot dial host to relay connection to: %v", err)
			return
		}

		go func() {
			io.Copy(relayConn, mpConn)
			closeCh <- true
		}()
		go func() {
			io.Copy(mpConn, relayConn)
			closeCh <- true
		}()
		<-closeCh
		return
	}

	go func() {
		io.Copy(os.Stdout, mpConn)
		closeCh <- true
	}()
	go func() {
		io.Copy(mpConn, os.Stdin)
		closeCh <- true
	}()
	<-closeCh
	// the defer will take care for a "slow close" to make sure that all of the
	// send and rx buffers are drained
}

func buildDialMpConn(otherArgs []string, ipv6OnlyFlag bool, ipv4OnlyFlag bool, multiplierFlag int, noAutoDetect bool) net.Conn {
	// we are not listening, we are dialing instead.
	dialers := make([]multipath.Dialer, 0)
	trackers := make([]multipath.StatsTracker, 0)

	if len(otherArgs) == 0 {
		return nil
	}
	// First collect up all the targets provided

	remoteTargets := make([]net.Addr, 0)
	for _, possibleTarget := range otherArgs {
		hostname, portString, err := net.SplitHostPort(possibleTarget)
		if err == nil {
			familyTest := net.ParseIP(hostname)
			if ipv6OnlyFlag {
				if familyTest.To4() != nil {
					// So it is a IPv4 address, so we must reject it
					continue
				}
			}

			if ipv4OnlyFlag {
				if familyTest.To4() == nil {
					// Then it's a IPv6 address, so we must reject it
					continue
				}
			}

			rT, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(hostname, portString))
			if err == nil {
				for i := 0; i < multiplierFlag; i++ {
					remoteTargets = append(remoteTargets, rT)
				}
			}
		} else {
			targetIPs, err := net.DefaultResolver.LookupIPAddr(context.Background(), hostname)
			if err != nil {
				log.Printf("Cannot resolve %v -> %v", possibleTarget, err)
				continue
			}
			for _, v := range targetIPs {
				if ipv6OnlyFlag {
					if v.IP.To4() != nil {
						// So it is a IPv4 address, so we must reject it
						continue
					}
				}

				if ipv4OnlyFlag {
					if v.IP.To4() == nil {
						// Then it's a IPv6 address, so we must reject it
						continue
					}
				}

				rT, err := net.ResolveTCPAddr("tcp", net.JoinHostPort(v.IP.String(), portString))
				if err == nil {
					for i := 0; i < multiplierFlag; i++ {
						remoteTargets = append(remoteTargets, rT)
					}
				}
			}
		}
	}

	if len(remoteTargets) == 0 {
		log.Fatalf("no valid targets given, please enter your remote hosts at the end of the command\n")
	}

	// Now we will build a list of compatible (IP Family wise) dialers
	allNetworkAddrs, err := net.InterfaceAddrs()
	if err == nil && !noAutoDetect {
		for _, addrCIDR := range allNetworkAddrs {
			interfaceIP, _, _ := net.ParseCIDR(addrCIDR.String())
			for _, rT := range remoteTargets {
				isInterfaceIPv6 := interfaceIP.To4() == nil
				isTargetv6 := rT.(*net.TCPAddr).IP.To4() == nil

				if isInterfaceIPv6 != isTargetv6 {
					// Wrong address family
					continue
				}

				td := newOutboundDialer(interfaceIP, rT)
				dialers = append(dialers, td)
				trackers = append(trackers, multipath.NullTracker{})
			}
		}
	} else {
		star := net.ParseIP("0.0.0.0")
		for _, rT := range remoteTargets {
			td := newOutboundDialer(star, rT)
			dialers = append(dialers, td)
			trackers = append(trackers, multipath.NullTracker{})
		}
	}

	mpDialer := multipath.NewDialer("bondcat", dialers)
	mpConn, err := mpDialer.DialContext(context.Background())
	if err != nil {
		fmt.Printf("Failed to build a multipath connection. (%v)\n", err)
		os.Exit(2)
	}
	return mpConn
}

func buildListenMpConn(listenPortFlag int, noAutoDetect bool, ipv6OnlyFlag bool, ipv4OnlyFlag bool) net.Conn {
	var listenPort uint16
	if listenPortFlag != -1 {
		listenPort = uint16(listenPortFlag)
	} else {
		l, err := net.Listen("tcp", "0.0.0.0:0")
		if err != nil {
			log.Fatalf("Could not calculate address to listen on (%s), try using -p <port number>", err)
		}
		listenPort = uint16(l.Addr().(*net.TCPAddr).Port)
		l.Close()
		log.Printf("Using port %d", listenPort)
	}

	bindAddrs := make([]string, 0)
	allNetworkAddrs, err := net.InterfaceAddrs()
	if err == nil && !noAutoDetect {
		for _, addrCIDR := range allNetworkAddrs {
			ip, _, err := net.ParseCIDR(addrCIDR.String())
			if ipv6OnlyFlag {
				if ip.To4() != nil {
					// So it is a IPv4 address, so we must reject it
					continue
				}
			}

			if ipv4OnlyFlag {
				if ip.To4() == nil {
					// Then it's a IPv6 address, so we must reject it
					continue
				}
			}

			if err == nil {
				bindAddrs = append(bindAddrs, net.JoinHostPort(ip.String(), fmt.Sprint(listenPort)))
			}
		}
	} else {
		bindAddrs = append(bindAddrs, fmt.Sprintf("0.0.0.0:%d", listenPort))
	}

	listeners := make([]net.Listener, 0)
	trackers := make([]multipath.StatsTracker, 0)
	for _, addr := range bindAddrs {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			continue
		}
		listeners = append(listeners, l)
		trackers = append(trackers, multipath.NullTracker{})
	}

	mpListen := multipath.NewListener(listeners, trackers)
	mpConn, err := mpListen.Accept()
	if err != nil {
		fmt.Printf("Failed to Accept a multipath connection. %v", err)
		os.Exit(3)
	}
	return mpConn
}

type targetedDailer struct {
	localDialer net.Dialer
	remoteAddr  net.Addr
}

func newOutboundDialer(inputLocalAddr net.IP, inputRemoteAddr net.Addr) *targetedDailer {
	td := &targetedDailer{
		localDialer: net.Dialer{
			LocalAddr: &net.TCPAddr{
				IP:   inputLocalAddr,
				Port: 0,
			},
		},
		remoteAddr: inputRemoteAddr,
	}
	return td
}

func (td *targetedDailer) DialContext(ctx context.Context) (net.Conn, error) {
	conn, err := td.localDialer.DialContext(ctx, "tcp", td.remoteAddr.String())
	if err != nil {
		if verboseFlag {
			log.Printf("failed to dial to %v: %v", td.remoteAddr.String(), err)
		}
		return nil, err
	}
	if verboseFlag {
		log.Printf("Dialed to %v->%v", conn.LocalAddr(), td.remoteAddr.String())
	}

	return conn, err
}

func (td *targetedDailer) Label() string {
	return fmt.Sprintf("%s|%s", td.localDialer.LocalAddr, td.remoteAddr)
}

type LogTracker struct {
	Prefix string
}

func (st LogTracker) OnRecv(uint64)           {}
func (st LogTracker) OnSent(uint64)           {}
func (st LogTracker) OnRetransmit(uint64)     {}
func (st LogTracker) UpdateRTT(time.Duration) {}
