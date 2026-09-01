package lambda

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestAuditEventInstanceIDs(t *testing.T) {
	e := AuditEvent{
		ResourceLRNs: []string{
			"lrn:cloud:instance:aaa111",
			"lrn:cloud:api_key:zzz999",  // not an instance
			"lrn:cloud:instance:aaa111", // duplicate
		},
		AdditionalDetails: map[string]any{"instance_lrn": "lrn:cloud:instance:bbb222", "region": "us-east-1"},
	}
	got := e.InstanceIDs()
	if len(got) != 2 || got[0] != "aaa111" || got[1] != "bbb222" {
		t.Fatalf("InstanceIDs() = %v", got)
	}
	if ids := (AuditEvent{ResourceLRNs: []string{"lrn:cloud:instance:", "nonsense"}}).InstanceIDs(); len(ids) != 0 {
		t.Errorf("expected no ids, got %v", ids)
	}
}

func TestInstanceStartTimesPaginatesAndTakesEarliest(t *testing.T) {
	var gotStart, gotToken []string
	page := 0
	c := newMock(t, func(w http.ResponseWriter, r *http.Request) {
		gotStart = append(gotStart, r.URL.Query().Get("start"))
		gotToken = append(gotToken, r.URL.Query().Get("page_token"))
		page++
		if page == 1 {
			// Later event first, to prove we keep the earliest.
			_, _ = w.Write([]byte(`{"data":[
              {"event_id":"e2","event_time":"2026-09-01T12:00:00Z","resource_lrns":["lrn:cloud:instance:i1"],"action":"restarted"},
              {"event_id":"e1","event_time":"2026-09-01T10:00:00Z","resource_lrns":["lrn:cloud:instance:i1"],"action":"launched"}
            ],"page_token":"tok2"}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[
          {"event_id":"e3","event_time":"2026-08-30T08:30:00Z","additional_details":{"instance_lrn":"lrn:cloud:instance:i2"},"action":"launched"}
        ],"page_token":null}`))
	})
	since := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	got, err := c.InstanceStartTimes(context.Background(), since)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %v", got)
	}
	if want := time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC); !got["i1"].Equal(want) {
		t.Errorf("i1 = %s, want earliest %s", got["i1"], want)
	}
	if want := time.Date(2026, 8, 30, 8, 30, 0, 0, time.UTC); !got["i2"].Equal(want) {
		t.Errorf("i2 = %s, want %s", got["i2"], want)
	}
	if page != 2 {
		t.Errorf("expected 2 pages, got %d", page)
	}
	if gotStart[0] != "2026-08-01T00:00:00Z" {
		t.Errorf("start param = %q", gotStart[0])
	}
	if gotToken[0] != "" || gotToken[1] != "tok2" {
		t.Errorf("page tokens = %v", gotToken)
	}
}

func TestInstanceStartTimesStopsAtPageCap(t *testing.T) {
	pages := 0
	c := newMock(t, func(w http.ResponseWriter, r *http.Request) {
		pages++
		_, _ = w.Write([]byte(`{"data":[],"page_token":"always-more"}`))
	})
	if _, err := c.InstanceStartTimes(context.Background(), time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if pages != MaxAuditPages {
		t.Errorf("walked %d pages, want cap of %d", pages, MaxAuditPages)
	}
}

func TestInstanceStartTimesSurfacesAPIError(t *testing.T) {
	c := newMock(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"error":{"code":"global/forbidden","message":"no audit access"}}`))
	})
	got, err := c.InstanceStartTimes(context.Background(), time.Now().Add(-time.Hour))
	if err == nil {
		t.Fatal("expected error")
	}
	if got == nil {
		t.Error("should return a usable (empty) map alongside the error")
	}
}

func TestAuditEventsOmitsEmptyParams(t *testing.T) {
	var query string
	c := newMock(t, func(w http.ResponseWriter, r *http.Request) {
		query = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"data":[],"page_token":null}`))
	})
	if _, _, err := c.AuditEvents(context.Background(), time.Time{}, time.Time{}, ""); err != nil {
		t.Fatal(err)
	}
	if query != "" {
		t.Errorf("zero times and no token should send no query params, got %q", query)
	}
}
