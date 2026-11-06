package flags

import (
	"hash/fnv"
	"slices"
	"strconv"
)

// Resolve evaluates key for s against snap. It has no side effect.
//
// It applies the precedence of ADR 0029 decision 5, in this order:
//  0. An undeclared key gives false (error). A declared key with no
//     rule, or a nil or empty snapshot, gives the declared default (default).
//  1. A signed-in subject with a user rule gets that value (targeting_match).
//  2. A global switch that is on gives true (static).
//  3. A signed-in subject whose role is in the rule gets true (targeting_match).
//  4. A signed-in subject whose bucket is below the percentage gets true (split).
//  5. Otherwise the value is false (static).
//
// An active test override (see flagstest.Override) decides first, with
// the reason static.
func Resolve(snap *Snapshot, key string, s Subject) Resolution {
	if r, ok := lookupOverride(key); ok {
		return r
	}
	return resolve(snap, key, s)
}

// resolve is Resolve without the override table.
func resolve(snap *Snapshot, key string, s Subject) Resolution {
	if snap == nil {
		return resolution(false, ReasonError)
	}
	decl, declared := snap.declared[key]
	if !declared {
		return resolution(false, ReasonError)
	}
	rule, ok := snap.rules[key]
	if !ok {
		return resolution(decl.Default, ReasonDefault)
	}
	signedIn := !s.Anonymous
	if signedIn {
		if on, ok := rule.Users[s.UserID]; ok {
			return resolution(on, ReasonTargetingMatch)
		}
	}
	if rule.Enabled {
		return resolution(true, ReasonStatic)
	}
	if signedIn && s.Role != "" {
		if _, found := slices.BinarySearch(rule.Roles, s.Role); found {
			return resolution(true, ReasonTargetingMatch)
		}
	}
	if signedIn && rule.Percentage > 0 && bucket(key, s.UserID) < rule.Percentage {
		return resolution(true, ReasonSplit)
	}
	return resolution(false, ReasonStatic)
}

// bucket returns fnv32a(key + ":" + strconv.FormatInt(userID, 10)) % 100
// (ADR 0029 decision 5.2). The key is in the hash, so each flag picks other
// users. The percentage is not, so a raise keeps every user who was on.
func bucket(key string, userID int64) int {
	var buf [80]byte // a valid key (48), ":", and an int64 (20) fit
	b := append(buf[:0], key...)
	b = append(b, ':')
	b = strconv.AppendInt(b, userID, 10)
	h := fnv.New32a()
	_, _ = h.Write(b)
	return int(h.Sum32() % 100)
}
