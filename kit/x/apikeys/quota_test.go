package apikeys

import (
	"testing"
	"time"
)

func TestMinuteBurstRefillAndIsolation(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	s := testStore(t, &now)
	for range 60 {
		if wait := s.takeMinute(1, 60, s.now()); wait != 0 {
			t.Fatalf("burst wait = %s", wait)
		}
	}
	if wait := s.takeMinute(1, 60, s.now()); wait != time.Second {
		t.Fatalf("wait = %s", wait)
	}
	if wait := s.takeMinute(2, 60, s.now()); wait != 0 {
		t.Fatalf("other user wait = %s", wait)
	}
	now = now.Add(time.Second)
	if wait := s.takeMinute(1, 60, s.now()); wait != 0 {
		t.Fatal(wait)
	}
	if wait := s.takeMinute(1, 60, s.now()); wait != time.Second {
		t.Fatal(wait)
	}
	now = now.Add(-time.Hour)
	if wait := s.takeMinute(1, 60, s.now()); wait > time.Second {
		t.Fatalf("backward clock wait = %s", wait)
	}
	now = now.Add(2 * time.Hour)
	s.sweep(now)
	if len(s.minute) != 0 {
		t.Fatal("sweep retained full buckets")
	}
}

func TestMinuteCap(t *testing.T) {
	now := time.Now()
	s := testStore(t, &now)
	for i := int64(1); i <= maxMinuteUsers+10; i++ {
		s.takeMinute(i, 60, now)
	}
	if len(s.minute) != maxMinuteUsers {
		t.Fatalf("entries = %d", len(s.minute))
	}
}

func TestDayArithmetic(t *testing.T) {
	zone := time.FixedZone("west", -6*60*60)
	now := time.Date(2026, 11, 6, 17, 59, 59, 500000000, zone)
	if utcDay(now).Format("2006-01-02") != "2026-11-06" || midnightSeconds(now) != 1 {
		t.Fatal("incorrect UTC day or reset")
	}
	s := testStore(t, &now)
	who := userDay{1, utcDay(now)}
	s.days[who] = &dayUsage{base: 999, local: 1}
	if s.cachedUsed(who) != 1000 {
		t.Fatal("base and local must both count")
	}
	now = now.Add(time.Second)
	if s.cachedUsed(userDay{1, utcDay(now)}) != 0 {
		t.Fatal("old counts crossed midnight")
	}
	if s.cachedUsed(who) != 1000 {
		t.Fatal("old counts must remain until flushed")
	}
	if midnightSeconds(now) != 86400 {
		t.Fatalf("reset = %d", midnightSeconds(now))
	}
}
