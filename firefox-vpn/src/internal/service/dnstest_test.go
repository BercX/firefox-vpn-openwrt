package service

import (
	"context"
	"errors"
	"net/http"
	"net/netip"
	"testing"

	"firefox-vpn/internal/resolver"
)

func TestDNSTestEndpointComparesWithSystem(t *testing.T) {
	addr := startTestDNS(t, [4]byte{23, 235, 42, 9})
	old := systemDNS
	systemDNS = func(_ context.Context, host string) (resolver.Result, error) {
		if host == "vpn.mozilla.org" {
			return resolver.Result{}, errors.New("blocked")
		}
		return resolver.Result{Addrs: []netip.Addr{netip.MustParseAddr("10.10.34.35")}, Source: "system"}, nil
	}
	t.Cleanup(func() { systemDNS = old })
	d := newTestDaemon(t)
	c := newDashboardClient(t, d)
	rec := c.do(http.MethodPost, "/api/dns/test", map[string]any{"servers": []string{"udp://" + addr}})
	if rec.Code != http.StatusOK {
		t.Fatalf("DNS test: %d %s", rec.Code, rec.Body)
	}
	out := decodeBody[struct {
		Results []dnsTestHost `json:"results"`
	}](t, rec)
	if len(out.Results) != 4 {
		t.Fatalf("expected four names, got %+v", out.Results)
	}
	for _, r := range out.Results {
		if len(r.Addrs) != 1 || r.Addrs[0] != "23.235.42.9" || r.Error != "" {
			t.Errorf("%s: configured answer %+v", r.Host, r)
		}
		if r.Host == "vpn.mozilla.org" {
			if r.SystemError != "blocked" || r.Differs || r.Filtered {
				t.Errorf("Guardian system answer %+v", r)
			}
		} else if !r.Differs || !r.Filtered {
			t.Errorf("%s: block-page answer was not flagged: %+v", r.Host, r)
		}
	}
	if rec := c.do(http.MethodPost, "/api/dns/test", map[string]any{"servers": []string{"quic://1.1.1.1"}}); rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid server: %d", rec.Code)
	}
}
