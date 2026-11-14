package live

import (
	"sync"
	"time"
)

type readState int

const (
	readOK readState = iota
	readGap
	readReset
	readClosed
)

type appendResult int

const (
	appendOK appendResult = iota
	appendGap
	appendStale
)

// One stream owns each sub and calls read in sequence.
type sub struct {
	wake  chan struct{}
	next  int64
	reset bool
}

type feed struct {
	key       Key
	mu        sync.RWMutex
	epoch     int64
	seq       int64
	ring      *ring
	subs      map[*sub]struct{}
	refs      int
	idleSince time.Time
	ready     chan struct{}
	loadErr   error
	closed    bool
}

func newFeed(key Key, maxPatches int) *feed {
	return &feed{key: key, ring: newRing(maxPatches), subs: make(map[*sub]struct{}), ready: make(chan struct{})}
}

func (f *feed) head() Cursor {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return Cursor{Epoch: f.epoch, Seq: f.seq}
}

func (f *feed) append(epoch, seq int64, frame []byte) appendResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	if epoch < f.epoch {
		return appendStale
	}
	empty := f.epoch == 0 && f.seq == 0
	if f.closed || seq != f.seq+1 || (epoch != f.epoch && !empty) {
		return appendGap
	}
	f.ring.push(seq, frame)
	f.epoch, f.seq = epoch, seq
	f.wakeAll()
	return appendOK
}

func (f *feed) reset(epoch int64) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ring.clear()
	f.epoch, f.seq = epoch, 0
	for s := range f.subs {
		s.reset = true
	}
	f.wakeAll()
}

func (f *feed) subscribe(next int64) *sub {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := &sub{wake: make(chan struct{}, 1), next: next}
	f.subs[s] = struct{}{}
	if f.closed {
		wakeSub(s)
	}
	return s
}

func (f *feed) unsubscribe(s *sub) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.subs, s)
}

func (f *feed) read(s *sub, dst [][]byte) (int, readState) {
	f.mu.RLock()
	defer f.mu.RUnlock()
	if f.closed {
		return 0, readClosed
	}
	if s.reset {
		s.reset = false
		return 0, readReset
	}
	n, gap := f.ring.copyFrom(s.next, dst)
	if gap {
		return 0, readGap
	}
	s.next += int64(n)
	return n, readOK
}

func (f *feed) close() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	f.wakeAll()
}

// wakeAll requires the write lock.
func (f *feed) wakeAll() {
	for s := range f.subs {
		wakeSub(s)
	}
}

func wakeSub(s *sub) {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}
