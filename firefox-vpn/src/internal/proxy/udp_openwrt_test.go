package proxy

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

type udpTestOpener func(string) (net.Conn, error)

func (f udpTestOpener) OpenTunnel(s string) (net.Conn, error) { return f(s) }

func TestOpenWrtClosedControllerDoesNotRebuild(t *testing.T) {
	c := &Controller{closed: true, refreshSession: func() error { t.Error("closed controller restarted network activity"); return nil }}
	if err := c.rebuildSession(nil); !errors.Is(err, ErrNoUsableSession) {
		t.Fatalf("closed rebuild: %v", err)
	}
}

func TestOpenWrtUDPDNSUsesTunnelAndValidatesReplies(t *testing.T) {
	query := dnsmessage.Message{Header: dnsmessage.Header{ID: 42, RecursionDesired: true}, Questions: []dnsmessage.Question{{Name: dnsmessage.MustNewName("example.com."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}}
	q, _ := query.Pack()
	query.Response = true
	answer, _ := query.Pack()
	var count atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/dns-message" {
			t.Error("not a DoH request")
		}
		got, _ := io.ReadAll(r.Body)
		if !bytes.Equal(got, q) {
			t.Error("query was changed")
		}
		w.Header().Set("Content-Type", "application/dns-message")
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "https://invalid.example/", http.StatusFound)
		case "/oversized":
			w.Write(make([]byte, maxDNSMessage+1))
		case "/mismatch":
			changed := bytes.Clone(answer)
			changed[1]++
			w.Write(changed)
		default:
			w.Write(answer)
		}
	}))
	defer server.Close()
	client := newUDPDNSClient(udpTestOpener(func(authority string) (net.Conn, error) {
		count.Add(1)
		return net.Dial("tcp", authority)
	}))
	defer client.CloseIdleConnections()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	client.Transport.(*http.Transport).TLSClientConfig = &tls.Config{RootCAs: roots}
	got, err := queryUDPDNS(context.Background(), client, server.URL+"/dns-query", q)
	if err != nil || !bytes.Equal(got, answer) || count.Load() == 0 {
		t.Fatalf("DNS tunnel failed: %x %v", got, err)
	}
	for _, path := range []string{"/redirect", "/oversized", "/mismatch"} {
		if _, err := queryUDPDNS(context.Background(), client, server.URL+path, q); err == nil {
			t.Fatalf("accepted invalid reply %s", path)
		}
	}
	if _, err := queryUDPDNS(context.Background(), client, server.URL, []byte("bad")); err == nil {
		t.Fatal("accepted malformed DNS")
	}
	if _, err := queryUDPDNS(context.Background(), newUDPDNSClient(nil), server.URL, q); err == nil {
		t.Fatal("fell back to direct DNS")
	}
}

func TestOpenWrtDatagramParsing(t *testing.T) {
	packet := []byte{0, 0, 0, 3, 11, 'e', 'x', 'a', 'm', 'p', 'l', 'e', '.', 'c', 'o', 'm', 0, 53, 1, 2, 3}
	target, header, payload, err := parseSOCKSDatagram(packet)
	if err != nil || target != "example.com:53" || len(header) != len(packet)-3 || !bytes.Equal(payload, []byte{1, 2, 3}) {
		t.Fatalf("datagram parse: %s %v", target, err)
	}
	for i := 0; i < len(header); i++ {
		if _, _, _, err := parseSOCKSDatagram(packet[:i]); err == nil {
			t.Fatalf("accepted truncated packet %d", i)
		}
	}
	packet[2] = 1
	if _, _, _, err := parseSOCKSDatagram(packet); err == nil {
		t.Fatal("accepted UDP fragments")
	}
}

func TestOpenWrtUDPAssociationLifetimeAndSource(t *testing.T) {
	for _, stopListener := range []bool{false, true} {
		s := NewSOCKSServer(refusingOpener{}, time.Second, 0, 8)
		s.Auth = SOCKSCredentials("user", "pw")
		opened := make(chan string, 8)
		s.DNSUpstream = udpTestOpener(func(target string) (net.Conn, error) {
			opened <- target
			return nil, errors.New("fixture refuses upstream")
		})
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		go ServeSOCKS(ln, s)
		ctrl, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer ctrl.Close()
		ctrl.SetDeadline(time.Now().Add(5 * time.Second))
		ctrl.Write([]byte{5, 1, 2})
		var auth [2]byte
		io.ReadFull(ctrl, auth[:])
		if auth != [2]byte{5, 2} {
			t.Fatal("auth not required")
		}
		ctrl.Write([]byte{1, 4, 'u', 's', 'e', 'r', 2, 'p', 'w'})
		io.ReadFull(ctrl, auth[:])
		if auth[1] != 0 {
			t.Fatal("auth failed")
		}
		ctrl.Write([]byte{5, 3, 0, 1, 0, 0, 0, 0, 0, 0})
		var reply [10]byte
		if _, err := io.ReadFull(ctrl, reply[:]); err != nil {
			t.Fatal(err)
		}
		if reply[1] != 0 {
			t.Fatalf("association rejected %x", reply)
		}
		addr := &net.UDPAddr{IP: net.IP(reply[4:8]), Port: int(binary.BigEndian.Uint16(reply[8:]))}
		udp, err := net.DialUDP("udp", nil, addr)
		if err != nil {
			t.Fatal(err)
		}
		defer udp.Close()
		query := dnsmessage.Message{Header: dnsmessage.Header{ID: 42}, Questions: []dnsmessage.Question{{Name: dnsmessage.MustNewName("example.com."), Type: dnsmessage.TypeA, Class: dnsmessage.ClassINET}}}
		payload, _ := query.Pack()
		packet := append([]byte{0, 0, 0, 1, 203, 0, 113, 1, 0, 53}, payload...)
		udp.Write(packet)
		select {
		case target := <-opened:
			if target != "1.1.1.1:443" {
				t.Fatalf("DNS not translated to DoH: %s", target)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("DNS query did not reach upstream")
		}
		foreign, err := net.DialUDP("udp", nil, addr)
		if err != nil {
			t.Fatal(err)
		}
		foreign.Write(packet)
		foreign.Close()
		packet[8], packet[9] = 1, 187 // Same valid payload, non-DNS destination.
		udp.Write(packet)
		select {
		case target := <-opened:
			t.Fatalf("foreign source or non-DNS UDP was forwarded: %s", target)
		case <-time.After(100 * time.Millisecond):
		}
		if stopListener {
			ln.Close()
			ctrl.SetReadDeadline(time.Now().Add(2 * time.Second))
			if _, err := ctrl.Read(reply[:]); err != io.EOF {
				t.Fatalf("listener shutdown did not close association: %v", err)
			}
		} else {
			ctrl.Close()
		}
		deadline := time.Now().Add(2 * time.Second)
		for len(s.connSlots) != 0 && time.Now().Before(deadline) {
			time.Sleep(time.Millisecond)
		}
		if len(s.connSlots) != 0 {
			t.Fatal("association did not release its connection slot")
		}
		packet[8], packet[9] = 0, 53
		udp.Write(packet)
		select {
		case target := <-opened:
			t.Fatalf("closed association forwarded DNS: %s", target)
		case <-time.After(100 * time.Millisecond):
		}
	}
}
