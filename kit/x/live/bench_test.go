package live

import "testing"

// fanoutBody prepares every allocation before the measured append and reads.
func fanoutBody() func() {
	f := newFeed("notes", 1024)
	subs := make([]*sub, 1000)
	for i := range subs {
		subs[i] = f.subscribe(1)
	}
	dst := make([][]byte, 1)
	frame := patchFrame(10, 1, []byte(`[{"op":"touch","id":"1"}]`))
	seq := int64(0)
	return func() {
		seq++
		if f.append(10, seq, frame) != appendOK {
			panic("append failed")
		}
		for _, s := range subs {
			<-s.wake
			if n, state := f.read(s, dst); n != 1 || state != readOK {
				panic("read failed")
			}
		}
	}
}

func BenchmarkFanout1000(b *testing.B) {
	body := fanoutBody()
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		body()
	}
}
