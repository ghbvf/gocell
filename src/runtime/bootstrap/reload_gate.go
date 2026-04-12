package bootstrap

import "sync"

// reloadGate prevents new config reload callbacks from entering once shutdown
// begins and exposes a drained signal for in-flight callbacks.
type reloadGate struct {
	mu            sync.Mutex
	shuttingDown  bool
	inFlight      int
	drained       chan struct{}
	drainedClosed bool
}

func newReloadGate() *reloadGate {
	return &reloadGate{drained: make(chan struct{})}
}

func (g *reloadGate) TryEnter() bool {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.shuttingDown {
		return false
	}

	g.inFlight++
	return true
}

func (g *reloadGate) Leave() {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.inFlight == 0 {
		return
	}

	g.inFlight--
	if g.shuttingDown && g.inFlight == 0 {
		g.closeDrainedLocked()
	}
}

func (g *reloadGate) BeginShutdown() <-chan struct{} {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.shuttingDown = true
	if g.inFlight == 0 {
		g.closeDrainedLocked()
	}

	return g.drained
}

func (g *reloadGate) closeDrainedLocked() {
	if g.drainedClosed {
		return
	}

	close(g.drained)
	g.drainedClosed = true
}
