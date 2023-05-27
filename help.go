package main

import (
	"flag"
	"fmt"
)

func initHelp() {
	flag.Usage = func() {
		fmt.Printf(`|\---/|
| o_o | Bondcat (%v), the TCP connection bonding swiss army knife
 \_^_/
Usage: bondcat [options] [hostname]:[port] [hostname]:[port] [hostname]:[port]...

Options for bondcat:

	-4		Use IPv4 only for DNS resolution and automatic binding
	-6		Use IPv6 only for DNS resolution and automatic binding
	-a		Disable automatic source connection/binding for every IP on the machine
	-no-auto-detect
	-b		Write and read 0x00 as fast as possible upon connections
	-benchmark
	-i		Close connection after n seconds of inactivity
	-idle-timeout
	-l		Listen for bondcat connections
	-listen
	-p		Port to listen on, if empty a random one is picked and printed
	-multiplier	How many extra connections per endpoint should be started
	-x
	-relay		Reverse Proxy all incoming connections to TCP hostname:port provided
	-v		Verbose/Error printing
	-verbose

Usage info:

	Bondcat can only work when the underlying network connectivity allows for it to
	use more than one NIC/Link at once.

	In the case of WAN links that involve link aggregation, This is likely a case of
	setting the -multiplier flag to above 1 against one hostname:port

	For LAN connectivity directing traffic is a little more complex depending on the
	exact situation you are in.
	
	If you are coming from a setup of "1xFast -> 2xSlow" then it's best to start the
	listener on the Fast host, and then connect with the Slow machine, This should
	automatically use both NICs to connect to the faster single link system.

	If you understand how to set custom routes to leverage maximum multipathing then you
	can disable the automatic bind/connect logic by using -a and then bondcat will only
	connect from exactly what you told it to. Otherwise bondcat will try and connect to
	the endpoints provided using every compatible IP address it can find. This may not
	result in the best performance by default.

Licence:

	Bondcat is licenced under Apache License v2
`, "0.1")
	}
}
