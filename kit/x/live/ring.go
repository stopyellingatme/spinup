package live

type ringEntry struct {
	seq   int64
	frame []byte
}

type ring struct {
	entries []ringEntry
	start   int
	count   int
	size    int64
}

func newRing(maxPatches int) *ring {
	if maxPatches <= 0 {
		panic("live: ring capacity must be positive")
	}
	return &ring{entries: make([]ringEntry, maxPatches)}
}

func (r *ring) push(seq int64, frame []byte) {
	if r.count > 0 && seq != r.last()+1 {
		panic("live: ring sequence is not consecutive")
	}
	if r.count == len(r.entries) {
		r.dropOldest()
	}
	i := (r.start + r.count) % len(r.entries)
	r.entries[i] = ringEntry{seq: seq, frame: frame}
	r.count++
	r.size += int64(len(frame))
}

func (r *ring) first() int64 {
	if r.count == 0 {
		return 0
	}
	return r.entries[r.start].seq
}

func (r *ring) last() int64 {
	if r.count == 0 {
		return 0
	}
	return r.entries[(r.start+r.count-1)%len(r.entries)].seq
}

func (r *ring) len() int     { return r.count }
func (r *ring) bytes() int64 { return r.size }

func (r *ring) dropOldest() int64 {
	if r.count == 0 {
		return 0
	}
	n := int64(len(r.entries[r.start].frame))
	r.entries[r.start] = ringEntry{}
	r.start = (r.start + 1) % len(r.entries)
	r.count--
	r.size -= n
	return n
}

func (r *ring) copyFrom(seq int64, dst [][]byte) (n int, gap bool) {
	if r.count == 0 {
		return 0, false
	}
	if seq < r.first() {
		return 0, true
	}
	if seq > r.last() {
		return 0, false
	}
	offset := int(seq - r.first())
	n = min(r.count-offset, len(dst))
	for i := 0; i < n; i++ {
		dst[i] = r.entries[(r.start+offset+i)%len(r.entries)].frame
	}
	return n, false
}

func (r *ring) clear() {
	clear(r.entries)
	r.start, r.count, r.size = 0, 0, 0
}
