package service

import (
	"context"
	"firefox-vpn/internal/controlplane"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"firefox-vpn/internal/routing"
	"firefox-vpn/internal/securefile"
)

// writeTestGeoIP writes a geoip.dat with the given tags and networks into
// the daemon's routing folder (the v2ray protobuf layout: GeoIPList ->
// GeoIP{country_code, cidr{ip, prefix}}).
func writeTestGeoIP(t *testing.T, d *Daemon, tags map[string][]string) {
	t.Helper()
	field := func(b []byte, num int, data []byte) []byte {
		b = binaryUvarint(b, uint64(num)<<3|2)
		b = binaryUvarint(b, uint64(len(data)))
		return append(b, data...)
	}
	var list []byte
	for tag, cidrs := range tags {
		entry := field(nil, 1, []byte(tag))
		for _, c := range cidrs {
			p := netip.MustParsePrefix(c)
			cidr := field(nil, 1, p.Addr().AsSlice())
			cidr = binaryUvarint(binaryUvarint(cidr, 2<<3), uint64(p.Bits()))
			entry = field(entry, 2, cidr)
		}
		list = field(list, 1, entry)
	}
	if err := securefile.EnsurePrivateDir(d.paths.RoutingDir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d.paths.RoutingDir, routing.GeoIPFile), list, 0o600); err != nil {
		t.Fatal(err)
	}
}

func binaryUvarint(b []byte, v uint64) []byte {
	for v >= 0x80 {
		b = append(b, byte(v)|0x80)
		v >>= 7
	}
	return append(b, byte(v))
}

// Routing is off by default and applies live when the settings change; a
// rule naming a list that has not been downloaded leaves everything going
// through the proxy and says why. Another region swaps its presets in the
// Direct list for the old region's; the user's own rules stay.
func TestRoutingSettingsApplyLive(t *testing.T) {
	d := newTestDaemon(t)
	cfg := d.config()
	if cfg.Routing.Enabled || d.currentRouter() != nil {
		t.Fatal("routing must be off by default")
	}
	// The region's presets are in the Direct list itself.
	if cfg.Routing.Region != "ir" || strings.Join(cfg.Routing.Direct, " ") != "geosite:ir ext:iran.dat:ir geoip:ir geoip:private" {
		t.Fatalf("default region %q and direct rules %q", cfg.Routing.Region, cfg.Routing.Direct)
	}
	cfg.Routing.Enabled = true
	if _, err := d.updateSettings(cfg, nil); err != nil {
		t.Fatal(err)
	}
	if d.currentRouter() != nil {
		t.Fatal("without lists routing must fall back to Mozilla")
	}

	// No region: the old region's presets leave the list, private
	// networks come in, before the user's rules.
	writeTestGeoIP(t, d, map[string][]string{"private": {"192.168.0.0/16"}})
	cfg.Routing.Region = ""
	cfg.Routing.Direct = []string{"domain:shop.ir", "10.0.0.0/8"}
	cfg.Routing.Block = []string{"full:ads.example"}
	if _, err := d.updateSettings(cfg, nil); err != nil {
		t.Fatal(err)
	}
	router := d.currentRouter()
	if router == nil || d.config().Routing.Region != "" {
		t.Fatal("rules with the network list must work at once")
	}
	if got := strings.Join(d.config().Routing.Direct, " "); got != "geoip:private domain:shop.ir 10.0.0.0/8" {
		t.Fatalf("effective direct rules %q", got)
	}
	for host, want := range map[string]routing.Action{
		"www.shop.ir": routing.Direct, "10.1.2.3": routing.Direct, "192.168.1.1": routing.Direct,
		"ads.example": routing.Block, "example.com": routing.Proxy, "2001:db8::1": routing.Proxy,
	} {
		if got := router.Decide(context.Background(), host, nil); got != want {
			t.Errorf("%s: got %s, want %s", host, got, want)
		}
	}

	// A region whose tags the list has applies live too.
	writeTestGeoIP(t, d, map[string][]string{"private": {"192.168.0.0/16"}, "tr": {"78.160.0.0/11"}})
	cfg.Routing.Region = "tr"
	if _, err := d.updateSettings(cfg, nil); err != nil {
		t.Fatal(err)
	}
	if router := d.currentRouter(); router == nil || d.config().Routing.Region != "tr" || !strings.Contains(router.Summary, "geoip:tr=") {
		t.Fatalf("region tr: %+v", router)
	}

	// China: its usual lists (Loyalsoldier's) replace the default ones;
	// lists set by hand stay when the region changes.
	cfg.Routing.Region = "cn"
	if _, err := d.updateSettings(cfg, nil); err != nil {
		t.Fatal(err)
	}
	if got := d.config().Routing; got.GeoSiteURL != routing.RegionSource("cn").GeoSiteURL || got.GeoIPURL != routing.RegionSource("cn").GeoIPURL {
		t.Fatalf("China's lists: %s %s", got.GeoSiteURL, got.GeoIPURL)
	}
	cfg = d.config()
	cfg.Routing.GeoSiteURL = "https://example.com/site.dat"
	cfg.Routing.Region = "tr"
	if _, err := d.updateSettings(cfg, nil); err != nil {
		t.Fatal(err)
	}
	if got := d.config().Routing.GeoSiteURL; got != "https://example.com/site.dat" {
		t.Fatalf("lists set by hand: %s", got)
	}
	cfg = d.config()

	cfg.Routing.Region = "xx"
	if _, err := d.updateSettings(cfg, nil); err == nil || !strings.Contains(err.Error(), "routing.region") {
		t.Fatalf("an unknown region must be refused: %v", err)
	}
	cfg.Routing.Region = "tr"

	cfg.Routing.Block = []string{"not a rule"}
	if _, err := d.updateSettings(cfg, nil); err == nil {
		t.Fatal("an invalid rule must be refused")
	}
}

func TestDestinationTest(t *testing.T) {
	d := newTestDaemon(t)
	c := newDashboardClient(t, d)
	test := func(target string) destinationTest {
		t.Helper()
		rec := c.do(http.MethodPost, "/api/routing/test", map[string]string{"target": target})
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", target, rec.Code, rec.Body)
		}
		return decodeBody[destinationTest](t, rec)
	}
	if got := test("digikala.com"); got.Route != "proxy" || !strings.Contains(got.Note, "off") {
		t.Fatalf("routing off: %+v", got)
	}
	writeTestGeoIP(t, d, map[string][]string{"private": {"192.168.0.0/16"}})
	cfg := d.config()
	cfg.Routing.Enabled = true
	cfg.Routing.Region = ""
	cfg.Routing.Direct = []string{"domain:shop.ir", "10.0.0.0/8"}
	cfg.Routing.Block = []string{"full:ads.example"}
	if _, err := d.updateSettings(cfg, nil); err != nil {
		t.Fatal(err)
	}
	for target, want := range map[string]destinationTest{
		"https://www.shop.ir/cart?x=1": {Host: "www.shop.ir", Port: "443", Route: "direct", Rule: "domain:shop.ir"},
		"10.1.2.3:9898":                {Host: "10.1.2.3", Port: "9898", Route: "direct", Rule: "10.0.0.0/8"},
		"192.168.1.1":                  {Host: "192.168.1.1", Route: "direct", Rule: "geoip:private"},
		"ads.example":                  {Host: "ads.example", Route: "block", Rule: "full:ads.example"},
		"example.com:9898":             {Host: "example.com", Port: "9898", Route: "proxy", PortRefused: true},
		"[2001:db8::1]:443":            {Host: "2001:db8::1", Port: "443", Route: "proxy"},
	} {
		if got := test(target); got != want {
			t.Errorf("%s: got %+v, want %+v", target, got, want)
		}
	}
	if rec := c.do(http.MethodPost, "/api/routing/test", map[string]string{"target": " "}); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty target: %d", rec.Code)
	}
}

func TestRoutingManualUpdate(t *testing.T) {
	d := newTestDaemon(t)
	writeTestGeoIP(t, d, map[string][]string{"private": {"192.168.0.0/16"}})
	data, err := os.ReadFile(filepath.Join(d.paths.RoutingDir, routing.GeoIPFile))
	if err != nil {
		t.Fatal(err)
	}
	// A valid empty geosite tag, plus the geoip fixture. No outside downloads.
	site := append([]byte{0x0a, 9, 0x0a, 7}, []byte("private")...)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/site.dat" {
			w.Write(site)
		} else {
			w.Write(data)
		}
	}))
	defer srv.Close()
	cfg := d.config()
	cfg.Routing.Region, cfg.Routing.Direct = "", nil
	cfg.Routing.GeoSiteURL, cfg.Routing.GeoIPURL = srv.URL+"/site.dat", srv.URL+"/ip.dat"
	if _, err := d.updateSettings(cfg, nil); err != nil {
		t.Fatal(err)
	}
	c := newDashboardClient(t, d)
	old := controlplane.Transport()
	controlplane.SetTransport(srv.Client().Transport)
	t.Cleanup(func() { controlplane.SetTransport(old) })
	rec := c.do(http.MethodPost, "/api/routing/update", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("manual update: %d %s", rec.Code, rec.Body)
	}
	view := decodeBody[routingView](t, rec)
	if view.Enabled || view.Updating || view.LastError != "" || view.LastCheck.IsZero() || len(view.Files) != 2 {
		t.Fatalf("manual update while disabled: %+v", view)
	}
}
