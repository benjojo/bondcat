module github.com/benjojo/bondcat

go 1.16

require (
	github.com/getlantern/golog v0.0.0-20210606115803-bce9f9fe5a5f
	github.com/getlantern/multipath v0.0.0-20201027015000-69ed0bd15259
	github.com/getlantern/netx v0.0.0-20211206143627-7ccfeb739cbd
)

replace github.com/getlantern/multipath => ./multipath
