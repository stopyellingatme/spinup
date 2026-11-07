package apikeys

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/stopyellingatme/spinup/kit/audit"
)

const maxMinuteUsers = 100000

type userDay struct {
	userID int64
	day    time.Time
}
type keyDay struct {
	keyID, userID int64
	day           time.Time
}
type dayUsage struct{ base, local int64 }

func utcDay(now time.Time) time.Time {
	now = now.UTC()
	return time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
}

func midnightSeconds(now time.Time) int64 {
	d := utcDay(now).AddDate(0, 0, 1).Sub(now)
	return max(1, int64((d+time.Second-1)/time.Second))
}

// takeMinute applies GCRA while quotaMu is held. Rejection does not change the bucket.
func (s *Store) takeMinute(userID int64, limit int, now time.Time) time.Duration {
	every := int64(time.Minute / time.Duration(limit))
	ns := now.UnixNano()
	tat := min(max(s.minute[userID], ns), ns+int64(limit)*every)
	wait := tat - ns - int64(limit-1)*every
	if wait > 0 {
		return time.Duration(wait)
	}
	if _, ok := s.minute[userID]; !ok && len(s.minute) >= maxMinuteUsers {
		s.evictMinute(ns)
	}
	s.minute[userID] = tat + every
	return 0
}

func (s *Store) evictMinute(now int64) {
	var victim, least int64
	n := 0
	for id, tat := range s.minute {
		if tat <= now {
			delete(s.minute, id)
			return
		}
		if n == 0 || tat < least {
			victim, least = id, tat
		}
		n++
		if n == 16 {
			break
		}
	}
	delete(s.minute, victim)
}

func (s *Store) sweep(now time.Time) {
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	for id, tat := range s.minute {
		if tat <= now.UnixNano() {
			delete(s.minute, id)
		}
	}
}

const totalSQL = `SELECT COALESCE(sum(g.count),0)::bigint FROM api_key_usage g JOIN api_keys k ON k.id=g.key_id WHERE k.user_id=$1 AND g.day=$2::date`

func (s *Store) cachedUsed(who userDay) int64 {
	s.quotaMu.Lock()
	defer s.quotaMu.Unlock()
	if d := s.days[who]; d != nil {
		return d.base + d.local
	}
	return 0
}

// flush snapshots pending counts without holding quotaMu during database work.
// Local counts include the snapshot until commit. Failed writes return to pending.
func (s *Store) flush(ctx context.Context) error {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()
	s.quotaMu.Lock()
	pending := s.pending
	s.pending = make(map[keyDay]int64)
	userIDs := make([]int64, 0, len(s.days))
	userDays := make([]time.Time, 0, len(s.days))
	for who := range s.days {
		userIDs = append(userIDs, who.userID)
		userDays = append(userDays, who.day)
	}
	s.quotaMu.Unlock()
	keyIDs := make([]int64, 0, len(pending))
	keyDays := make([]time.Time, 0, len(pending))
	counts := make([]int64, 0, len(pending))
	for k, count := range pending {
		keyIDs = append(keyIDs, k.keyID)
		keyDays = append(keyDays, k.day)
		counts = append(counts, count)
	}
	totals := make(map[userDay]int64, len(userIDs))
	err := audit.InTx(ctx, s.pool, func(tx pgx.Tx) error {
		// Empty arrays still check the schema at startup. The key order prevents
		// concurrent replicas from locking the same usage rows in different orders.
		if _, err := tx.Exec(ctx, `INSERT INTO api_key_usage(key_id,day,count)
   SELECT k.id,v.day,v.count FROM unnest($1::bigint[],$2::date[],$3::bigint[]) AS v(key_id,day,count)
   JOIN api_keys k ON k.id=v.key_id ORDER BY k.id,v.day
   ON CONFLICT(key_id,day) DO UPDATE SET count=api_key_usage.count+excluded.count`, keyIDs, keyDays, counts); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE api_keys SET last_used_at=now() WHERE id=ANY($1::bigint[])`, keyIDs); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `SELECT v.user_id,v.day,COALESCE(sum(g.count),0)::bigint
   FROM unnest($1::bigint[],$2::date[]) AS v(user_id,day)
   LEFT JOIN api_keys k ON k.user_id=v.user_id
   LEFT JOIN api_key_usage g ON g.key_id=k.id AND g.day=v.day
   GROUP BY v.user_id,v.day`, userIDs, userDays)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var who userDay
			var total int64
			if err := rows.Scan(&who.userID, &who.day, &total); err != nil {
				return err
			}
			who.day = utcDay(who.day)
			totals[who] = total
		}
		return rows.Err()
	})
	s.quotaMu.Lock()
	if err != nil {
		for k, count := range pending {
			s.pending[k] += count
		}
		err = storeFailure("flush the usage", err)
	} else {
		for k, count := range pending {
			who := userDay{k.userID, k.day}
			if d := s.days[who]; d != nil {
				d.local -= count
			}
		}
		today := utcDay(s.now())
		for who, total := range totals {
			if who.day.Before(today) {
				delete(s.days, who)
			} else {
				s.days[who].base = total
			}
		}
	}
	s.quotaMu.Unlock()
	s.mu.Lock()
	s.lastErr = err
	s.mu.Unlock()
	if err != nil {
		s.logger.WarnContext(ctx, "apikeys: cannot flush the usage. Try: check the database and migrations.", "error", err)
	}
	return err
}

// Start flushes once and starts the loop. Database failures affect Ready but do not fail Start.
// A nonpositive interval selects five seconds. Start can be called only once.
func (s *Store) Start(ctx context.Context, interval time.Duration) error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.started || s.closed {
		return &storeError{message: "apikeys: the flush loop has already started or closed.\nTry: call Start once before Close."}
	}
	if interval <= 0 {
		interval = 5 * time.Second
	}
	s.started = true
	runCtx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.done = make(chan struct{})
	s.flushWithTimeout(runCtx)
	go s.run(runCtx, interval)
	return nil
}

func (s *Store) flushWithTimeout(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_ = s.flush(ctx)
}

func (s *Store) run(ctx context.Context, interval time.Duration) {
	defer close(s.done)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	sweep := time.NewTicker(time.Minute)
	defer sweep.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.flushWithTimeout(ctx)
		case <-sweep.C:
			s.sweep(s.now())
		}
	}
}

// Ready returns the last flush error. It fails before the first successful flush.
func (s *Store) Ready(_ context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastErr
}

// Close stops the loop and flushes pending counts. Repeated calls are safe and retry failed flushes.
func (s *Store) Close(ctx context.Context) error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closeFlushed {
		return nil
	}
	s.closed = true
	if s.cancel != nil {
		s.cancel()
		select {
		case <-s.done:
		case <-ctx.Done():
			return &storeError{cause: ctx.Err(), message: fmt.Sprintf("apikeys: cannot stop the flush loop: %v\nTry: allow more time for shutdown.", ctx.Err())}
		}
	}
	err := s.flush(ctx)
	s.closeFlushed = err == nil
	return err
}
