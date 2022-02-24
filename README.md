# bondcat
![bondcat banner](/.github/bondcat.png)

Bondcat is a [netcat](https://en.wikipedia.org/wiki/Netcat) like system utility that has the ability to combine multiple TCP connections for better transfer speeds, but also better reliability (Assuming the connections take different network paths).

---

# Download and Install

If your Linux distribution or OS has not packaged bondcat yet, You can check the [release page](https://github.com/benjojo/bondcat/releases) for the latest release. Downloading one of the pre-built binaries and copying it to `/usr/local/bin/bondcat` should effectively install bondcat.

## Install from Source

A simple `go build` should build a working binary!

# Usage

```
|\---/|
| o_o | Bondcat (0.1), the TCP connection bonding swiss army knife
 \_^_/
Usage: bondcat [options] [hostname]:[port] [hostname]:[port] [hostname]:[port]...

Options for bondcat:

        -4              Use IPv4 only for DNS resolution and automatic binding
        -6              Use IPv6 only for DNS resolution and automatic binding
        -a              Disable automatic source connection/binding for every IP on the machine
        -no-auto-detect
        -b              Write and read 0x00 as fast as possible upon connections
        -benchmark
        -i              Close connetion after duration of inactivity (for example "10s")
        -idle-timeout
        -l              Listen for bondcat connections
        -listen
        -p              Port to listen on, if empty a random one is picked and printed
        -multiplier     How many extra connections per endpoint should be started
        -x
        -relay          Reverse Proxy all incoming connections to TCP hostname:port provided
        -v              Verbose/Error printing
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
```        

# Understanding applicable scenarios

Bondcat needs to establish connections that go down different network paths to provide better throughput and reliability. There are a handful of ways to do this that depending on how your network looks between the two systems you are trying to communicate with.

The most simple one is two point to point links. Where each interface has their own IP subnet.

```
+-------+   eth0      +-------+
|       +-------------+       |
|  A    |             |  B    |
|       +-------------+       |
+-------+   eth1      +-------+
```

This is great since it's simple and obvious to your operating system that the two TCP connections will go down different ethernet cables, Assuming both of these links are gigabit ethernet. This would result in a throughput of 2gbit/s of I/O performance.

```
+------+ 1GbE   +--+       +-----+
|      +--------+  |       |     |
| A    |  eth0  |SW|  10GbE|  B  |
|      |  eth1  |  +-------+     |
|      +--------+  |  eth1 |     |
+------+ 1GbE   +--+       +-----+
```

A much more common setup is that you have a ethernet switch in the middle with different connection speeds between peers, In a standard setup this would limit your throughput to 1gbit/s. If provided with correct IP subnetting, this could trivially move 2gbit/s.

```
+-------------+         |
| DSL Modem   +---------+----------------------+
+-----+-------+         |                      |
      |                 I                      |
      |                 N                      |
      |                 T                      |
+-----+-------+         E              +-------+-------+
|             |         R              |               |
|  Client     |         N              |   Server      |
|             |         E              |               |
+--------+-+--+         T              +-------+-------+
         |L|            |                      |
         |T|            |                      |
         |E+------------+----------------------+
         +-+            |
```

Bondcat can be used for more than just better throughput. If you want your connection to stay online even if your normal connection fails, even if just for a moment. 

This could be used to have ultra reliable SSH for example. Or if you are doing critical data streaming that need rapid failover.
