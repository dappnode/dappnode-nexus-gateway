package metering

import (
	"context"
	"errors"
	"time"

	"github.com/dappnode/dappnode-nexus-gateway/pkg/domain"
)

// lease is a credit hold measured on this process's clock. Metering sends a
// duration rather than a timestamp, so clock skew (an enclave) cannot make a
// hold look expired or valid when it is not.
type lease struct {
	expires time.Time
	length  time.Duration
}

func newLease(sent time.Time, ms int64) lease {
	length := time.Duration(ms) * time.Millisecond
	return lease{expires: sent.Add(length), length: length}
}

// KeepReservation renews a hold at a third of its remaining time until stop is
// called. Transient failures retry within the lease, never more often than a
// tenth of it or once per second. onLost runs when Metering says the hold is
// gone, or when it expires without a successful renewal.
func (c *Client) KeepReservation(parent context.Context, id string, onLost func()) func() {
	value, ok := c.reservations.LoadAndDelete(id)
	if !ok {
		return func() {} // Older Metering responses do not advertise leases.
	}
	held := value.(lease)
	floor := min(time.Second, held.length/10)
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	lost := func() {
		if ctx.Err() == nil {
			onLost()
		}
	}
	go func() {
		defer close(done)
		for {
			remaining := time.Until(held.expires)
			if remaining <= 0 {
				lost()
				return
			}
			timer := time.NewTimer(min(max(remaining/3, floor), remaining))
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
			if time.Until(held.expires) <= 0 {
				continue
			}
			requestCtx, stop := context.WithDeadline(ctx, held.expires)
			var result reserveResponse
			sent := time.Now()
			err := c.postJSON(requestCtx, "/internal/metering/reservations/"+id+"/renew", struct{}{}, &result, 0)
			stop()
			switch {
			case err == nil && result.ReservationID == id && result.LeaseMS > 0:
				if renewed := newLease(sent, result.LeaseMS); renewed.expires.After(held.expires) {
					held = renewed
				}
			case rejectedByMetering(err):
				lost() // Completed, expired, or unknown: retrying cannot restore it.
				return
			}
		}
	}()
	return func() { cancel(); <-done }
}

// rejectedByMetering reports a 4xx answer; transport errors and 5xx are
// transient and retried while the lease lasts.
func rejectedByMetering(err error) bool {
	var gwErr *domain.GatewayError
	if !errors.As(err, &gwErr) {
		return false
	}
	status, ok := gwErr.Metadata["metering_status"].(int)
	return ok && status >= 400 && status < 500
}
