package lambda

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Progress is called on every poll with the current status and elapsed time.
type Progress func(status, ip string, elapsed time.Duration)

// WaitActive polls the instance until it is active with a public IP.
func (c *Client) WaitActive(ctx context.Context, id string, interval, timeout time.Duration, progress Progress) (Instance, error) {
	start := time.Now()
	for {
		inst, err := c.Instance(ctx, id)
		if err != nil {
			return inst, err
		}
		if inst.Status == StatusActive && inst.IP != "" {
			return inst, nil
		}
		switch inst.Status {
		case StatusTerminated, StatusTerminating, StatusUnhealthy, StatusPreempted:
			return inst, fmt.Errorf("instance %s went %s", id, inst.Status)
		}
		if time.Since(start) > timeout {
			return inst, fmt.Errorf("timed out after %s waiting for active (status=%s)", timeout.Round(time.Second), inst.Status)
		}
		if progress != nil {
			progress(inst.Status, inst.IP, time.Since(start))
		}
		select {
		case <-ctx.Done():
			return inst, ctx.Err()
		case <-time.After(interval):
		}
	}
}

// LaunchRetry calls Launch, retrying on insufficient capacity until deadline.
// onRetry is called before each sleep. A zero retryFor means a single attempt.
func (c *Client) LaunchRetry(ctx context.Context, req LaunchRequest, retryFor, interval time.Duration, onRetry func(err error, until time.Time)) ([]string, error) {
	deadline := time.Now().Add(retryFor)
	for {
		ids, err := c.Launch(ctx, req)
		if err == nil {
			return ids, nil
		}
		if !IsCode(err, CodeInsufficientCapacity) || time.Now().Add(interval).After(deadline) {
			return nil, err
		}
		if onRetry != nil {
			onRetry(err, deadline)
		}
		select {
		case <-ctx.Done():
			return nil, errors.Join(ctx.Err(), err)
		case <-time.After(interval):
		}
	}
}
