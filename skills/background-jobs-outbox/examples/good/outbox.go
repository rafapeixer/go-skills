package main

import (
	"math/rand/v2"
	"time"
)

const (
	outboxChannel = "outbox"

	insertOrderSQL  = `INSERT INTO orders (customer_id, total_cents) VALUES ($1, $2) RETURNING id`
	insertOutboxSQL = `INSERT INTO outbox (topic, payload) VALUES ($1, $2)`

	// claimSQL leases a batch in one autocommit statement. SKIP LOCKED lets
	// many relays claim disjoint rows without waiting on each other; pushing
	// available_at forward is the lease, so a relay that dies mid-batch hands
	// its rows back when the lease ends. No transaction stays open while the
	// relay talks to the broker.
	claimSQL = `
UPDATE outbox SET attempts = attempts + 1,
       available_at = now() + $2::float8 * interval '1 second'
WHERE id IN (
    SELECT id FROM outbox
    WHERE sent_at IS NULL AND dead_at IS NULL AND available_at <= now()
    ORDER BY available_at, id
    LIMIT $1
    FOR UPDATE SKIP LOCKED)
RETURNING id, topic, payload, attempts`

	markSentSQL  = `UPDATE outbox SET sent_at = now() WHERE id = $1`
	markRetrySQL = `UPDATE outbox SET available_at = now() + $2::float8 * interval '1 second',
       last_error = $3 WHERE id = $1`
	markDeadSQL = `UPDATE outbox SET dead_at = now(), last_error = $2 WHERE id = $1`

	claimBatch  = 100
	leaseFor    = 30 * time.Second
	maxAttempts = 8
	baseDelay   = time.Second
	maxDelay    = 10 * time.Minute
)

// event is one claimed outbox row.
type event struct {
	ID       int64
	Topic    string
	Payload  []byte
	Attempts int
}

// afterFailure decides what happens to an event whose publish failed on its
// attempts-th try: retry after a capped, jittered exponential delay, or go to
// the dead state once maxAttempts is reached, where an alert and a human
// take over instead of an endless retry loop.
func afterFailure(attempts int) (dead bool, delay time.Duration) {
	if attempts >= maxAttempts {
		return true, 0
	}
	d := min(baseDelay<<max(attempts-1, 0), maxDelay)
	// Equal jitter: at least half the delay, so a broker outage does not
	// turn into a synchronized retry storm when it recovers.
	return false, d/2 + rand.N(d/2+1)
}
