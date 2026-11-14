package live

import (
	"sync"
	"testing"
)

func TestFeed(t *testing.T) {
	f := newFeed("notes", 2)
	s1, s2 := f.subscribe(1), f.subscribe(1)
	defer f.unsubscribe(s1)
	defer f.unsubscribe(s2)
	frame := []byte("patch")
	if got := f.append(10, 1, frame); got != appendOK {
		t.Fatal(got)
	}
	if got := f.head(); got != (Cursor{10, 1}) {
		t.Fatal(got)
	}
	for _, s := range []*sub{s1, s2} {
		select {
		case <-s.wake:
		default:
			t.Fatal("missing wake")
		}
		dst := make([][]byte, 2)
		if n, state := f.read(s, dst); n != 1 || state != readOK || &dst[0][0] != &frame[0] || s.next != 2 {
			t.Fatal(n, state, s.next, dst)
		}
		if n, state := f.read(s, dst); n != 0 || state != readOK {
			t.Fatal(n, state)
		}
	}
	for _, tt := range []struct {
		epoch, seq int64
		want       appendResult
	}{
		{9, 2, appendStale}, {11, 2, appendGap}, {10, 1, appendGap}, {10, 3, appendGap}, {10, 2, appendOK}, {10, 3, appendOK},
	} {
		if got := f.append(tt.epoch, tt.seq, frame); got != tt.want {
			t.Fatalf("%+v: %v", tt, got)
		}
	}
	slow := f.subscribe(1)
	if n, state := f.read(slow, make([][]byte, 1)); n != 0 || state != readGap {
		t.Fatal(n, state)
	}
	f.unsubscribe(slow)
}

func TestFeedResetAndClose(t *testing.T) {
	f := newFeed("notes", 2)
	s1, s2 := f.subscribe(1), f.subscribe(1)
	f.reset(20)
	if got := f.head(); got != (Cursor{20, 0}) {
		t.Fatal(got)
	}
	for _, s := range []*sub{s1, s2} {
		select {
		case <-s.wake:
		default:
			t.Fatal("reset did not wake")
		}
		if n, state := f.read(s, nil); n != 0 || state != readReset {
			t.Fatal(n, state)
		}
		if n, state := f.read(s, nil); n != 0 || state != readOK {
			t.Fatal(n, state)
		}
	}
	fresh := f.subscribe(1)
	if _, state := f.read(fresh, nil); state != readOK {
		t.Fatal("new sub inherited reset")
	}
	if got := f.append(20, 1, []byte("new")); got != appendOK {
		t.Fatal(got)
	}
	// Wakes are already full. Neither reset nor close can block.
	f.reset(21)
	f.close()
	f.close()
	for _, s := range []*sub{s1, s2, fresh, f.subscribe(1)} {
		select {
		case <-s.wake:
		default:
			t.Fatal("close did not wake")
		}
		if n, state := f.read(s, nil); n != 0 || state != readClosed {
			t.Fatal(n, state)
		}
	}
}

func TestFeedResetDropsHistory(t *testing.T) {
	f := newFeed("notes", 3)
	if f.append(10, 1, []byte("old")) != appendOK {
		t.Fatal("initial append failed")
	}
	s := f.subscribe(1)
	f.reset(11)
	if f.ring.len() != 0 || f.ring.bytes() != 0 {
		t.Fatal("reset retained frames")
	}
	if f.append(10, 2, nil) != appendStale || f.append(11, 2, nil) != appendGap || f.append(11, 1, []byte("new")) != appendOK {
		t.Fatal("invalid append result after reset")
	}
	dst := make([][]byte, 2)
	if _, state := f.read(s, dst); state != readReset {
		t.Fatal(state)
	}
	if n, state := f.read(s, dst); n != 1 || state != readOK || string(dst[0]) != "new" {
		t.Fatal(n, state, dst)
	}
}

func TestFeedConcurrentReaders(t *testing.T) {
	f := newFeed("notes", 100)
	frame := []byte("patch")
	var wg sync.WaitGroup
	for range 8 {
		s := f.subscribe(1)
		wg.Go(func() {
			defer f.unsubscribe(s)
			dst := make([][]byte, 16)
			for s.next <= 100 {
				<-s.wake
				for {
					n, state := f.read(s, dst)
					if state != readOK {
						t.Errorf("state: %v", state)
						return
					}
					if n == 0 {
						break
					}
				}
			}
		})
	}
	for seq := int64(1); seq <= 100; seq++ {
		if f.append(10, seq, frame) != appendOK {
			t.Fatal("append failed")
		}
	}
	wg.Wait()
}

func TestFeedBootstrapState(t *testing.T) {
	f := newFeed("tasks:7", 4)
	if f.key != "tasks:7" || f.refs != 0 || !f.idleSince.IsZero() || f.loadErr != nil || f.head() != (Cursor{}) {
		t.Fatal("invalid bootstrap state")
	}
	select {
	case <-f.ready:
		t.Fatal("feed is ready before bootstrap")
	default:
	}
	if got := f.append(10, 2, nil); got != appendGap {
		t.Fatal("empty feed accepted a gap")
	}
	if got := f.append(10, 1, []byte("first")); got != appendOK {
		t.Fatal(got)
	}
	if got := f.append(10, 2, []byte("second")); got != appendOK {
		t.Fatal(got)
	}
	s := f.subscribe(2)
	dst := make([][]byte, 1)
	if n, state := f.read(s, dst); n != 1 || state != readOK || string(dst[0]) != "second" || s.next != 3 {
		t.Fatal(n, state, dst, s.next)
	}
	f.unsubscribe(s)
	if f.append(10, 3, []byte("third")) != appendOK {
		t.Fatal("append after unsubscribe failed")
	}
	select {
	case <-s.wake:
		t.Fatal("unsubscribed stream received a wake")
	default:
	}
}
