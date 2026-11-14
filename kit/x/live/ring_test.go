package live

import (
	"testing"
)

func TestRing(t *testing.T) {
	r := newRing(3)
	dst := make([][]byte, 4)
	if r.first() != 0 || r.last() != 0 || r.len() != 0 || r.bytes() != 0 || r.dropOldest() != 0 {
		t.Fatal("nonempty new ring")
	}
	if n, gap := r.copyFrom(0, dst); n != 0 || gap {
		t.Fatal(n, gap)
	}
	frames := [][]byte{[]byte("a"), []byte("bb"), []byte("ccc"), []byte("dddd")}
	for i, f := range frames {
		r.push(int64(i+5), f)
	}
	if r.first() != 6 || r.last() != 8 || r.len() != 3 || r.bytes() != 9 {
		t.Fatalf("ring after wrap: %+v", r)
	}
	if n, gap := r.copyFrom(5, dst); n != 0 || !gap {
		t.Fatal(n, gap)
	}
	if n, gap := r.copyFrom(6, dst); n != 3 || gap || &dst[0][0] != &frames[1][0] || string(dst[2]) != "dddd" {
		t.Fatal(n, gap, dst)
	}
	if n, gap := r.copyFrom(7, dst[:1]); n != 1 || gap || string(dst[0]) != "ccc" {
		t.Fatal(n, gap, dst)
	}
	if n, gap := r.copyFrom(9, dst); n != 0 || gap {
		t.Fatal(n, gap)
	}
	if freed := r.dropOldest(); freed != 2 || r.bytes() != 7 || r.first() != 7 {
		t.Fatal(freed, r.bytes(), r.first())
	}
	r.clear()
	if r.first() != 0 || r.last() != 0 || r.len() != 0 || r.bytes() != 0 {
		t.Fatal("clear failed")
	}
	r.push(100, frames[0])
	if r.first() != 100 {
		t.Fatal(r.first())
	}
	r.dropOldest()
	if r.first() != 0 || r.last() != 0 || r.bytes() != 0 {
		t.Fatal("drop final frame failed")
	}
}

func TestRingSequencePanic(t *testing.T) {
	for _, seq := range []int64{3, 4, 6} {
		t.Run(string(rune('0'+seq)), func(t *testing.T) {
			r := newRing(1)
			r.push(4, []byte("x"))
			defer func() {
				if recover() == nil {
					t.Fatal("push accepted a nonconsecutive sequence")
				}
			}()
			r.push(seq, []byte("y"))
		})
	}
}
