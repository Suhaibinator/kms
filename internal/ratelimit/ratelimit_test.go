package ratelimit

import (
	"testing"
	"time"
)

func TestRefund(t *testing.T) {
	l := New(1, 1)
	if !l.Allow("k") {
		t.Fatal("reservation should succeed")
	}
	l.Refund("k")
	if !l.Allow("k") {
		t.Fatal("refund did not restore the token")
	}
	// Refund never overfills the bucket.
	l.Refund("k")
	l.Refund("k")
	if !l.Allow("k") {
		t.Fatal("single token expected after refunds")
	}
	if l.Allow("k") {
		t.Fatal("refund overfilled the bucket beyond burst")
	}
	// Refunding an unknown key is a no-op.
	l.Refund("unknown")
}

func TestRefill(t *testing.T) {
	now := time.Unix(0, 0)
	l := New(60, 2) // 60/min = 1/sec, burst 2
	l.SetNow(func() time.Time { return now })

	if !l.Allow("k") {
		t.Fatalf("first of burst should be allowed")
	}
	if !l.Allow("k") {
		t.Fatalf("second of burst should be allowed")
	}
	if l.Allow("k") {
		t.Fatalf("third immediate request should be denied")
	}
	now = now.Add(time.Second) // refill one token
	if !l.Allow("k") {
		t.Fatalf("request after 1s refill should be allowed")
	}
	if l.Allow("k") {
		t.Fatalf("no tokens should remain")
	}

	// Distinct keys have independent buckets.
	if !l.Allow("other") {
		t.Fatalf("other key should have a full bucket")
	}
}

func TestTakeIsAllOrNothing(t *testing.T) {
	now := time.Unix(0, 0)
	l := New(60, 10)
	l.SetNow(func() time.Time { return now })

	if !l.Take("k", 4) {
		t.Fatal("4 of 10 should be granted")
	}
	if l.Take("k", 7) {
		t.Fatal("7 of remaining 6 must be refused")
	}
	// The refused Take must not have consumed anything.
	if !l.Take("k", 6) {
		t.Fatal("remaining 6 should still be available after the refused take")
	}
	if l.Take("k", 1) {
		t.Fatal("bucket should be empty")
	}
	if !l.Take("k", 0) || !l.Take("k", -3) {
		t.Fatal("non-positive take always succeeds")
	}
	now = now.Add(3 * time.Second)
	if !l.Take("k", 3) {
		t.Fatal("3 tokens should have refilled after 3s at 1/s")
	}
	if l.Take("k", 1) {
		t.Fatal("bucket should be empty again")
	}
}

func TestBoundedMapDeniesWhenFull(t *testing.T) {
	now := time.Unix(0, 0)
	l := New(60, 1)
	l.SetNow(func() time.Time { return now })
	l.maxBuckets = 2

	if !l.Allow("a") || !l.Allow("b") {
		t.Fatal("first two keys should be admitted")
	}
	// Both tracked keys are drained (actively throttled): a third key cannot be
	// admitted without growing the map, so it is refused.
	if l.Allow("c") {
		t.Fatal("third key admitted while the map was full of throttled keys")
	}
	if len(l.buckets) != 2 {
		t.Fatalf("bucket count = %d, want 2", len(l.buckets))
	}
	// Once the tracked keys refill to burst they are idle and get swept.
	now = now.Add(2 * time.Second)
	if !l.Allow("c") {
		t.Fatal("idle buckets should have been swept to admit a new key")
	}
}
