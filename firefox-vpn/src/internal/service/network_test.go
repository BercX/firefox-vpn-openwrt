package service

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"

	"firefox-vpn/internal/resolver"
)

// startTestDNS answers every A query with ip.
func startTestDNS(t *testing.T, ip [4]byte) string {
	t.Helper()
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 1500)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			var msg dnsmessage.Message
			if msg.Unpack(buf[:n]) != nil || len(msg.Questions) == 0 {
				continue
			}
			reply := dnsmessage.Message{Header: dnsmessage.Header{ID: msg.Header.ID, Response: true}, Questions: msg.Questions}
			if q := msg.Questions[0]; q.Type == dnsmessage.TypeA {
				reply.Answers = []dnsmessage.Resource{{
					Header: dnsmessage.ResourceHeader{Name: q.Name, Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET, TTL: 60},
					Body:   &dnsmessage.AResource{A: ip},
				}}
			}
			packed, _ := reply.Pack()
			_, _ = pc.WriteTo(packed, addr)
		}
	}()
	return pc.LocalAddr().String()
}

func TestSettingsApplyDNS(t *testing.T) {
	t.Cleanup(func() { resolver.Set(nil) })
	d := newTestDaemon(t)
	cfg := d.config()
	cfg.DNS.Servers = []string{"https://1.1.1.1/dns-query", "tls://9.9.9.9"}
	if _, err := d.updateSettings(cfg, nil); err != nil {
		t.Fatal(err)
	}
	if got := resolver.Current().String(); !strings.Contains(got, "https://1.1.1.1/dns-query") || !strings.Contains(got, "tls://9.9.9.9:853") {
		t.Fatalf("installed resolver = %s", got)
	}
	if view, _ := d.StatusView(); !strings.Contains(view.DNS, "1.1.1.1") {
		t.Fatalf("status DNS = %q", view.DNS)
	}
	addr := startTestDNS(t, [4]byte{23, 235, 42, 9})
	cfg.DNS.Servers, cfg.DNS.FallbackToSystem = []string{"udp://" + addr}, false
	if _, err := d.updateSettings(cfg, nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := resolver.Current().Lookup(ctx, "example.invalid")
	if err != nil || len(result.Addrs) != 1 || result.Addrs[0].String() != "23.235.42.9" {
		t.Fatalf("configured DNS lookup: %+v %v", result, err)
	}
	cfg.DNS.Servers = []string{"dns.google"}
	if _, err := d.updateSettings(cfg, nil); err == nil {
		t.Fatal("a host name without a scheme must be rejected")
	}
	cfg.DNS.Servers = []string{}
	if _, err := d.updateSettings(cfg, nil); err != nil {
		t.Fatal(err)
	}
	if !resolver.Current().System() {
		t.Fatal("an empty list must restore the system resolver")
	}
}
