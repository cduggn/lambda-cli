package lambda

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func newMock(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(srv.URL, "testkey")
}

func TestLaunchSendsBodyAndParsesIDs(t *testing.T) {
	var got LaunchRequest
	c := newMock(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer testkey" {
			t.Errorf("auth header = %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/instance-operations/launch" || r.Method != http.MethodPost {
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"data":{"instance_ids":["abc123"]}}`))
	})
	ids, err := c.Launch(context.Background(), LaunchRequest{
		RegionName: "us-east-1", InstanceTypeName: "gpu_1x_a100_sxm4", SSHKeyNames: []string{"k"},
		Image: &ImageSpec{Family: "lambda-stack-22-04"}, UserData: "#cloud-config\n",
	})
	if err != nil || len(ids) != 1 || ids[0] != "abc123" {
		t.Fatalf("ids=%v err=%v", ids, err)
	}
	if got.Image == nil || got.Image.Family != "lambda-stack-22-04" || got.Image.ID != "" {
		t.Errorf("image not serialised as family: %+v", got.Image)
	}
	if got.UserData != "#cloud-config\n" || got.Name != "" {
		t.Errorf("body = %+v", got)
	}
}

func TestOmitsEmptyOptionalFields(t *testing.T) {
	b, _ := json.Marshal(LaunchRequest{RegionName: "r", InstanceTypeName: "t", SSHKeyNames: []string{"k"}})
	for _, k := range []string{"image", "user_data", "name", "hostname", "file_system_names"} {
		if strings.Contains(string(b), `"`+k+`"`) {
			t.Errorf("empty %s should be omitted: %s", k, b)
		}
	}
}

func TestAPIErrorEnvelope(t *testing.T) {
	c := newMock(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"error":{"code":"global/invalid-api-key","message":"bad","suggestion":"fix it"}}`))
	})
	_, err := c.Instances(context.Background())
	if !IsCode(err, CodeInvalidAPIKey) {
		t.Fatalf("want invalid-api-key, got %v", err)
	}
	if e := err.(*APIError); e.Status != 401 || e.Suggestion != "fix it" {
		t.Errorf("error fields: %+v", e)
	}
}

func TestLaunchRetryOnInsufficientCapacity(t *testing.T) {
	var calls int32
	c := newMock(t, func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"error":{"code":"` + CodeInsufficientCapacity + `","message":"none"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"instance_ids":["ok"]}}`))
	})
	retries := 0
	ids, err := c.LaunchRetry(context.Background(), LaunchRequest{}, time.Second, 10*time.Millisecond, func(error, time.Time) { retries++ })
	if err != nil || ids[0] != "ok" || retries != 2 {
		t.Fatalf("ids=%v err=%v retries=%d", ids, err, retries)
	}
}

func TestLaunchRetryGivesUpAtDeadline(t *testing.T) {
	c := newMock(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"code":"` + CodeInsufficientCapacity + `","message":"none"}}`))
	})
	_, err := c.LaunchRetry(context.Background(), LaunchRequest{}, 30*time.Millisecond, 10*time.Millisecond, nil)
	if !IsCode(err, CodeInsufficientCapacity) {
		t.Fatalf("want capacity error, got %v", err)
	}
}

func TestLaunchRetryDoesNotRetryOtherErrors(t *testing.T) {
	var calls int32
	c := newMock(t, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"error":{"code":"global/invalid-parameters","message":"bad"}}`))
	})
	_, err := c.LaunchRetry(context.Background(), LaunchRequest{}, time.Hour, time.Millisecond, nil)
	if err == nil || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}

func TestWaitActive(t *testing.T) {
	var polls int32
	c := newMock(t, func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&polls, 1)
		if n < 3 {
			_, _ = w.Write([]byte(`{"data":{"id":"i1","status":"booting"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"id":"i1","status":"active","ip":"1.2.3.4"}}`))
	})
	var progress int
	inst, err := c.WaitActive(context.Background(), "i1", time.Millisecond, time.Second, func(string, string, time.Duration) { progress++ })
	if err != nil || inst.IP != "1.2.3.4" || progress != 2 {
		t.Fatalf("inst=%+v err=%v progress=%d", inst, err, progress)
	}
}

func TestWaitActiveFailsOnTerminalStatus(t *testing.T) {
	c := newMock(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"id":"i1","status":"unhealthy"}}`))
	})
	if _, err := c.WaitActive(context.Background(), "i1", time.Millisecond, time.Second, nil); err == nil {
		t.Fatal("expected error for unhealthy instance")
	}
}

func TestInstanceTypesAndCapacity(t *testing.T) {
	c := newMock(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"gpu_1x_a100_sxm4":{"instance_type":{"name":"gpu_1x_a100_sxm4","price_cents_per_hour":129,"specs":{"vcpus":30}},"regions_with_capacity_available":[{"name":"us-east-1","description":"VA"}]}}}`))
	})
	types, err := c.InstanceTypes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	a := types["gpu_1x_a100_sxm4"]
	if a.InstanceType.PriceUSD() != 1.29 || !a.HasCapacity("us-east-1") || a.HasCapacity("us-west-1") {
		t.Errorf("parsed: %+v", a)
	}
}
