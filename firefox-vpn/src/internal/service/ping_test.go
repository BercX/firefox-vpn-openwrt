package service

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"firefox-vpn/internal/proxy"
	"firefox-vpn/internal/serverlist"
)

func TestFastestUsesCachedEligibleLocation(t *testing.T) {
	d := newTestDaemon(t)
	d.pings = []proxy.LocationPing{
		{Code: "US", PingMS: 1, Locked: true},
		{Code: "NL", PingMS: 40},
		{Code: "DE", Error: "unreachable"},
	}
	d.pingedAt = time.Now()
	if got := d.resolveFastestCountry(""); got != "NL" {
		t.Fatalf("fastest eligible country = %q, want NL", got)
	}
	d.pings[1].Error = "unreachable"
	if got := d.resolveFastestCountry(""); got != "" {
		t.Fatalf("no eligible country must use Recommended, got %q", got)
	}
	// An expired measurement must not keep results for unavailable servers.
	d.countries = []serverlist.Country{{Code: "NL"}}
	d.pingedAt = time.Now().Add(-pingReuseAge - time.Second)
	if got := d.resolveFastestCountry(""); got != "" || len(d.pings) != 0 {
		t.Fatalf("refresh must discard obsolete pings: country=%q pings=%+v", got, d.pings)
	}
}

func TestSettingsSetLocation(t *testing.T) {
	d := newTestDaemon(t)
	client := newDashboardClient(t, d)
	cfg := d.config()
	cfg.Proxy.Country = proxy.FastestCountry
	if rec := client.do(http.MethodPut, "/api/settings", map[string]any{"config": cfg}); rec.Code != http.StatusOK {
		t.Fatalf("got %d %s", rec.Code, rec.Body.String())
	}
	if got := d.config().Proxy.Country; got != proxy.FastestCountry {
		t.Fatalf("country = %q", got)
	}
}

func TestSetLocationEndpoint(t *testing.T) {
	d := newTestDaemon(t)
	c := newDashboardClient(t, d)
	for _, country := range []string{proxy.FastestCountry, "US", "recommended"} {
		if rec := c.do(http.MethodPost, "/api/location", map[string]string{"country": country}); rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", country, rec.Code, rec.Body)
		}
		want := country
		if country == "recommended" {
			want = ""
		}
		if got := d.config().Proxy.Country; got != want {
			t.Fatalf("country = %q, want %q", got, want)
		}
	}
	if rec := c.do(http.MethodPost, "/api/locations/ping", map[string]any{}); rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no enabled accounts") {
		t.Fatalf("ping without account: %d %s", rec.Code, rec.Body)
	}
}

func TestSubsetMeasurementKeepsFullCacheExpiry(t *testing.T) {
	d := newTestDaemon(t)
	d.countries = []serverlist.Country{{Code: "NL"}}
	d.pings = []proxy.LocationPing{{Code: "US", PingMS: 1}}
	d.pingedAt = time.Now().Add(-pingReuseAge - time.Second)
	expired := d.pingedAt
	d.measureLocations(context.Background(), "", []string{"NL"})
	if !d.pingedAt.Equal(expired) || len(d.pings) != 1 {
		t.Fatalf("subset must preserve unmeasured cache and its age: %v %+v", d.pingedAt, d.pings)
	}
	if got := d.resolveFastestCountry(""); got != "" || len(d.pings) != 0 {
		t.Fatalf("full refresh must discard obsolete pings: %q %+v", got, d.pings)
	}
}
