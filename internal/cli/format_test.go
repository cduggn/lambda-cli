package cli

import (
	"testing"
	"time"
)

func TestShortDur(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "<1m"},
		{30 * time.Second, "<1m"},
		{90 * time.Second, "2m"},
		{42 * time.Minute, "42m"},
		{5*time.Hour + 12*time.Minute, "5h12m"},
		{25 * time.Hour, "1d1h"},
		{5 * time.Hour, "5h"},
		{24 * time.Hour, "1d"},
		{30 * 24 * time.Hour, "30d"},
		{3*24*time.Hour + 4*time.Hour, "3d4h"},
		{-time.Hour, "<1m"},
	}
	for _, c := range cases {
		if got := shortDur(c.in); got != c.want {
			t.Errorf("shortDur(%s) = %q, want %q", c.in, got, c.want)
		}
	}
}
