package multipath

import (
	"fmt"
	"sync"
)

var cluedoLockTracker sync.Map

type debugMutex struct {
	internalLock sync.Mutex
	frameID      int
	gid          int
}

func (d *debugMutex) Lock(frameID int) {
	d.internalLock.Lock()
	d.frameID = frameID
	d.gid = debugGoID()
	cluedoLockTracker.Store(fmt.Sprintf("F:%d = %d", frameID), 1)
}

func (d *debugMutex) Unlock() {
	cluedoLockTracker.Delete(fmt.Sprintf("F:%d = %d", d.frameID, d.gid))
	d.internalLock.Unlock()
}
