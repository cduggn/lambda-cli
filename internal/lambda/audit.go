package lambda

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// AuditEvent is one entry from the account activity log.
type AuditEvent struct {
	EventID           string         `json:"event_id"`
	EventTime         time.Time      `json:"event_time"`
	ServiceName       string         `json:"service_name"`
	ResourceName      string         `json:"resource_name"`
	Action            string         `json:"action"`
	ResourceLRNs      []string       `json:"resource_lrns"`
	AdditionalDetails map[string]any `json:"additional_details"`
}

// InstanceIDs returns the instance ids this event refers to, from its resource
// LRNs and from additional_details.instance_lrn. LRNs look like
// "lrn:cloud:instance:<id>".
func (e AuditEvent) InstanceIDs() []string {
	var ids []string
	add := func(lrn string) {
		if id, ok := instanceIDFromLRN(lrn); ok {
			for _, seen := range ids {
				if seen == id {
					return
				}
			}
			ids = append(ids, id)
		}
	}
	for _, l := range e.ResourceLRNs {
		add(l)
	}
	if v, ok := e.AdditionalDetails["instance_lrn"].(string); ok {
		add(v)
	}
	return ids
}

func instanceIDFromLRN(lrn string) (string, bool) {
	const prefix = "lrn:cloud:instance:"
	if !strings.HasPrefix(lrn, prefix) {
		return "", false
	}
	id := strings.TrimPrefix(lrn, prefix)
	if id == "" {
		return "", false
	}
	return id, true
}

// AuditEvents returns one page of audit events in [start, end]. A zero time omits
// that bound. The returned token feeds the next call; empty means no more pages.
func (c *Client) AuditEvents(ctx context.Context, start, end time.Time, pageToken string) ([]AuditEvent, string, error) {
	q := url.Values{}
	if !start.IsZero() {
		q.Set("start", start.UTC().Format(time.RFC3339))
	}
	if !end.IsZero() {
		q.Set("end", end.UTC().Format(time.RFC3339))
	}
	if pageToken != "" {
		q.Set("page_token", pageToken)
	}
	path := "/audit-events"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	raw, err := c.doRaw(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, "", err
	}
	var env struct {
		Data      []AuditEvent `json:"data"`
		PageToken *string      `json:"page_token"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, "", fmt.Errorf("GET %s: decode: %w", path, err)
	}
	next := ""
	if env.PageToken != nil {
		next = *env.PageToken
	}
	return env.Data, next, nil
}

// MaxAuditPages bounds how many pages InstanceStartTimes will walk.
const MaxAuditPages = 25

// InstanceStartTimes returns the earliest audit-event time seen per instance id
// since the given time. Lambda's instance objects carry no launch timestamp, so
// the activity log is the only server-side source for uptime. The earliest event
// mentioning an instance is its launch, which avoids depending on the exact
// action name in the event catalog.
func (c *Client) InstanceStartTimes(ctx context.Context, since time.Time) (map[string]time.Time, error) {
	out := map[string]time.Time{}
	token := ""
	for page := 0; page < MaxAuditPages; page++ {
		events, next, err := c.AuditEvents(ctx, since, time.Time{}, token)
		if err != nil {
			return out, err
		}
		for _, e := range events {
			if e.EventTime.IsZero() {
				continue
			}
			for _, id := range e.InstanceIDs() {
				if prev, ok := out[id]; !ok || e.EventTime.Before(prev) {
					out[id] = e.EventTime
				}
			}
		}
		if next == "" {
			return out, nil
		}
		token = next
		select {
		case <-ctx.Done():
			return out, ctx.Err()
		default:
		}
	}
	return out, nil
}
