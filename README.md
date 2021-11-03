# bondcat
![bondcat banner](/.github/bondcat.png)

BondCat is a [netcat](https://en.wikipedia.org/wiki/Netcat) like system utility that has the ability to combine multipul TCP connections for better transfer speeds, but also better reliability (Assuming the connections take different network paths).

---

# Download and Install

Debian and Ubuntu users can add the apt repo for bondcat here:

```
TODO TODO TODO TODO TODO TODO TODO TODO TODO TODO TODO TODO TODO TODO TODO TODO TODO TODO TODO TODO TODO 
```

If your Linux distribution or OS has not packaged bondcat yet, You can check the [release page](https://github.com/benjojo/bondcat/releases) for the latest release. Downloading one of the pre-built binaries and copying it to `/usr/local/bin/bondcat` should effectively install bondcat.

## Install from Source

# Usage

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

BondCat can be used for more than just better throughput. If you want your connection to stay online even if your normal connection fails, even if just for a moment. 

This could be used to have ultra reliable SSH for example. Or if you are doing critical data streaming that need rapid failover.

```
               |
               |
               I
+-------+      N      +-------+
|       |      T      |       |
|   A   +------E------+   B   |
|       |      R      |       |
+-------+      N      +-------+
               E
               T
               |
               |
```

The final setup is more vauge. You may find (or know) that the connection inbetween A and B has ECMP/LACP hashing on the path. Each individual path may only have a small amount of capacity and so starting more than one connection may help overall throughput.