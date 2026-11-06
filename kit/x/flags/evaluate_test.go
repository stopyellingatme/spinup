package flags

import (
	"hash/fnv"
	"strconv"
	"testing"
	"time"
)

var testDeclared = Declared{
	"new_checkout": {Description: "The new checkout flow"},
	"dark_mode":    {Description: "Dark mode", Default: true, Public: true},
}

// userWithBucket returns the first user ID from 1 whose bucket for key
// meets pred.
func userWithBucket(t *testing.T, key string, pred func(int) bool) int64 {
	t.Helper()
	for id := int64(1); id < 100000; id++ {
		if pred(bucket(key, id)) {
			return id
		}
	}
	t.Fatal("no user ID found")
	return 0
}

func TestResolvePrecedence(t *testing.T) {
	const key = "new_checkout"
	member := Subject{UserID: 7, Role: "staff"}
	low := userWithBucket(t, key, func(b int) bool { return b < 30 })
	high := userWithBucket(t, key, func(b int) bool { return b >= 30 })

	tests := []struct {
		name  string
		snap  *Snapshot
		key   string
		subj  Subject
		value bool
		why   Reason
	}{
		{"undeclared key", NewSnapshot(testDeclared, map[string]Rule{"other": {Enabled: true}}, time.Time{}), "other", member, false, ReasonError},
		{"nil snapshot", nil, key, member, false, ReasonError},
		{"no rule gives default off", NewSnapshot(testDeclared, nil, time.Time{}), key, member, false, ReasonDefault},
		{"no rule gives default on", NewSnapshot(testDeclared, nil, time.Time{}), "dark_mode", member, true, ReasonDefault},
		{"step 1 user on", NewSnapshot(testDeclared, map[string]Rule{key: {Users: map[int64]bool{7: true}}}, time.Time{}), key, member, true, ReasonTargetingMatch},
		{"step 1 user off beats global on", NewSnapshot(testDeclared, map[string]Rule{key: {Enabled: true, Users: map[int64]bool{7: false}}}, time.Time{}), key, member, false, ReasonTargetingMatch},
		{"step 1 user off beats role", NewSnapshot(testDeclared, map[string]Rule{key: {Roles: []string{"staff"}, Users: map[int64]bool{7: false}}}, time.Time{}), key, member, false, ReasonTargetingMatch},
		{"step 2 global on", NewSnapshot(testDeclared, map[string]Rule{key: {Enabled: true}}, time.Time{}), key, member, true, ReasonStatic},
		{"step 2 global on beats default", NewSnapshot(testDeclared, map[string]Rule{"dark_mode": {Enabled: true}}, time.Time{}), "dark_mode", member, true, ReasonStatic},
		{"step 3 role", NewSnapshot(testDeclared, map[string]Rule{key: {Roles: []string{"staff", "manager"}}}, time.Time{}), key, member, true, ReasonTargetingMatch},
		{"step 3 other role", NewSnapshot(testDeclared, map[string]Rule{key: {Roles: []string{"manager"}}}, time.Time{}), key, member, false, ReasonStatic},
		{"step 3 empty role matches nothing", NewSnapshot(testDeclared, map[string]Rule{key: {Roles: []string{"manager"}}}, time.Time{}), key, Subject{UserID: 7}, false, ReasonStatic},
		{"step 4 split in", NewSnapshot(testDeclared, map[string]Rule{key: {Percentage: 30}}, time.Time{}), key, Subject{UserID: low}, true, ReasonSplit},
		{"step 4 split out", NewSnapshot(testDeclared, map[string]Rule{key: {Percentage: 30}}, time.Time{}), key, Subject{UserID: high}, false, ReasonStatic},
		{"step 4 hundred percent", NewSnapshot(testDeclared, map[string]Rule{key: {Percentage: 100}}, time.Time{}), key, Subject{UserID: high}, true, ReasonSplit},
		{"step 5 off", NewSnapshot(testDeclared, map[string]Rule{key: {}}, time.Time{}), key, member, false, ReasonStatic},
		{"step 5 stored off beats default on", NewSnapshot(testDeclared, map[string]Rule{"dark_mode": {}}, time.Time{}), "dark_mode", member, false, ReasonStatic},
		{"anonymous skips user rule", NewSnapshot(testDeclared, map[string]Rule{key: {Users: map[int64]bool{0: true}}}, time.Time{}), key, Subject{Anonymous: true}, false, ReasonStatic},
		{"anonymous skips role", NewSnapshot(testDeclared, map[string]Rule{key: {Roles: []string{"staff"}}}, time.Time{}), key, Subject{Role: "staff", Anonymous: true}, false, ReasonStatic},
		{"anonymous skips split", NewSnapshot(testDeclared, map[string]Rule{key: {Percentage: 100}}, time.Time{}), key, Subject{Anonymous: true}, false, ReasonStatic},
		{"anonymous gets global on", NewSnapshot(testDeclared, map[string]Rule{key: {Enabled: true, Users: map[int64]bool{0: false}}}, time.Time{}), key, Subject{Anonymous: true}, true, ReasonStatic},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Resolve(tt.snap, tt.key, tt.subj)
			want := Resolution{Value: tt.value, Variant: "off", Reason: tt.why}
			if tt.value {
				want.Variant = "on"
			}
			if got != want {
				t.Fatalf("Resolve = %+v, want %+v", got, want)
			}
		})
	}
}

func TestBucketMatchesFNV(t *testing.T) {
	for _, tc := range []struct {
		key string
		id  int64
	}{{"new_checkout", 1}, {"a", 0}, {"dark_mode", -5}, {"k", 9223372036854775807}} {
		h := fnv.New32a()
		_, _ = h.Write([]byte(tc.key + ":" + strconv.FormatInt(tc.id, 10)))
		want := int(h.Sum32() % 100)
		if got := bucket(tc.key, tc.id); got != want {
			t.Fatalf("bucket(%q, %d) = %d, want %d", tc.key, tc.id, got, want)
		}
	}
}

func TestBucketStable(t *testing.T) {
	first := bucket("new_checkout", 42)
	for range 100 {
		if got := bucket("new_checkout", 42); got != first {
			t.Fatalf("bucket changed: %d then %d", first, got)
		}
	}
	// Pinned value, so a change of the hash input breaks this test.
	h := fnv.New32a()
	_, _ = h.Write([]byte("new_checkout:42"))
	if want := int(h.Sum32() % 100); first != want {
		t.Fatalf("bucket = %d, want %d", first, want)
	}
}

func TestPercentageProperty(t *testing.T) {
	const key, n = "new_checkout", 10000
	on := func(pct int, id int64) bool {
		snap := NewSnapshot(testDeclared, map[string]Rule{key: {Percentage: pct}}, time.Time{})
		return Resolve(snap, key, Subject{UserID: id}).Value
	}
	prev := make([]bool, n+1)
	for _, pct := range []int{0, 1, 5, 10, 20, 25, 50, 75, 99, 100} {
		count := 0
		for id := int64(1); id <= n; id++ {
			v := on(pct, id)
			if prev[id] && !v {
				t.Fatalf("raising the percentage to %d removed user %d", pct, id)
			}
			prev[id] = v
			if v {
				count++
			}
		}
		share := float64(count) * 100 / n
		if share < float64(pct)-2 || share > float64(pct)+2 {
			t.Fatalf("percentage %d gave %.2f%% of users", pct, share)
		}
	}
}

func TestPercentageDependsOnKey(t *testing.T) {
	same := 0
	for id := int64(1); id <= 1000; id++ {
		if bucket("alpha", id) == bucket("beta", id) {
			same++
		}
	}
	if same > 100 {
		t.Fatalf("%d of 1000 users share a bucket across keys", same)
	}
}

func BenchmarkResolve(b *testing.B) {
	snap := NewSnapshot(testDeclared, map[string]Rule{"new_checkout": {Percentage: 50, Roles: []string{"manager"}}}, time.Time{})
	s := Subject{UserID: 4242, Role: "staff"}
	b.ReportAllocs()
	for b.Loop() {
		_ = Resolve(snap, "new_checkout", s)
	}
}
