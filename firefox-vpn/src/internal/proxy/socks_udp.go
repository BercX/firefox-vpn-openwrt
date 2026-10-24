// SPDX-License-Identifier: MIT
package proxy

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"sync"
	"time"

	"firefox-vpn/internal/logx"
	"golang.org/x/net/dns/dnsmessage"
)

const udpIdleTimeout = 60 * time.Second
const maxDNSMessage = 4096
const udpDoHURL = "https://1.1.1.1/dns-query"

// Each association belongs to one authenticated TCP peer and one UDP source
// port. The TCP connection and listener lifetime own every socket and stream.
func (s *SOCKSServer) serveUDP(ctrl net.Conn, atyp byte) {
	host, err := readSOCKSAddr(ctrl, atyp)
	var port [2]byte
	if err == nil {
		_, err = io.ReadFull(ctrl, port[:])
	}
	peer, peerOK := ctrl.RemoteAddr().(*net.TCPAddr)
	local, localOK := ctrl.LocalAddr().(*net.TCPAddr)
	if err != nil || !peerOK || !localOK {
		writeSOCKSReply(ctrl, socksReplyFailure, nil)
		return
	}
	requested := net.ParseIP(host)
	if requested == nil || (!requested.IsUnspecified() && !requested.Equal(peer.IP)) {
		writeSOCKSReply(ctrl, socksReplyNotAllow, nil)
		return
	}
	pc, err := net.ListenUDP("udp", &net.UDPAddr{IP: local.IP, Zone: local.Zone})
	if err != nil {
		writeSOCKSReply(ctrl, socksReplyFailure, nil)
		return
	}
	defer pc.Close()
	defer ctrl.Close()
	bound := pc.LocalAddr().(*net.UDPAddr)
	if err := writeSOCKSReply(ctrl, socksReplySuccess, &net.TCPAddr{IP: bound.IP, Port: bound.Port}); err != nil {
		return
	}
	ctrl.SetDeadline(time.Time{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { io.Copy(io.Discard, ctrl); cancel() }()
	go func() {
		select {
		case <-ctx.Done():
		case <-s.udpDone:
		}
		cancel()
		pc.Close()
		ctrl.Close()
	}()
	client := newUDPDNSClient(s.DNSUpstream)
	defer client.CloseIdleConnections()
	var workers sync.WaitGroup
	var dnsFailure sync.Once
	defer func() {
		cancel()
		pc.Close()
		ctrl.Close()
		workers.Wait()
	}()
	clientPort := int(binary.BigEndian.Uint16(port[:]))
	logged := false
	buffer := make([]byte, 65535)
	for {
		pc.SetReadDeadline(time.Now().Add(udpIdleTimeout))
		n, from, err := pc.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		if !from.IP.Equal(peer.IP) || (clientPort != 0 && from.Port != clientPort) {
			continue
		}
		target, header, payload, err := parseSOCKSDatagram(buffer[:n])
		if err != nil {
			continue
		}
		_, targetPort, _ := net.SplitHostPort(target)
		if targetPort != "53" {
			if !logged {
				logx.Warn("UDP DNS relay: non-DNS UDP blocked; use a UDP-capable VPN client=%s", logx.Addr(peer))
				logged = true
			}
			continue
		}
		if clientPort == 0 {
			clientPort = from.Port
		}
		select {
		case s.udpSlots <- struct{}{}:
		default:
			continue
		}
		query, replyHeader := bytes.Clone(payload), bytes.Clone(header)
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-s.udpSlots }()
			answer, err := queryUDPDNS(ctx, client, udpDoHURL, query)
			if err != nil {
				dnsFailure.Do(func() { logx.Warn("UDP DNS over HTTPS failed; no direct fallback err=%s", logx.Err(err)) })
				logx.Debug("UDP DNS query failed err=%s", logx.Err(err))
				return
			}
			pc.WriteToUDP(append(replyHeader, answer...), from)
		}()
	}
}

func parseSOCKSDatagram(packet []byte) (string, []byte, []byte, error) {
	if len(packet) < 4 || packet[0] != 0 || packet[1] != 0 || packet[2] != 0 {
		return "", nil, nil, fmt.Errorf("invalid or fragmented SOCKS datagram")
	}
	r := bytes.NewReader(packet[4:])
	host, err := readSOCKSAddr(r, packet[3])
	if err != nil || host == "" {
		return "", nil, nil, fmt.Errorf("invalid UDP address")
	}
	var port [2]byte
	if _, err := io.ReadFull(r, port[:]); err != nil {
		return "", nil, nil, err
	}
	p := binary.BigEndian.Uint16(port[:])
	if p == 0 {
		return "", nil, nil, fmt.Errorf("invalid UDP port")
	}
	offset := len(packet) - r.Len()
	return net.JoinHostPort(host, fmt.Sprint(p)), packet[:offset], packet[offset:], nil
}

func newUDPDNSClient(upstream tunnelOpener) *http.Client {
	return &http.Client{
		Timeout:       8 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport: &http.Transport{
			Proxy: nil, MaxConnsPerHost: 4, MaxIdleConns: 4, IdleConnTimeout: 30 * time.Second,
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				if upstream == nil {
					return nil, fmt.Errorf("DNS tunnel is unavailable")
				}
				// OpenTunnel has its own bounded open timeout. Never use a direct dial.
				return upstream.OpenTunnel(addr)
			},
		},
	}
}

func queryUDPDNS(ctx context.Context, client *http.Client, endpoint string, query []byte) ([]byte, error) {
	var q dnsmessage.Message
	if len(query) > maxDNSMessage || q.Unpack(query) != nil || q.Response || len(q.Questions) == 0 {
		return nil, fmt.Errorf("invalid DNS query")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(query))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/dns-message")
	req.Header.Set("Accept", "application/dns-message")
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if resp.StatusCode != 200 || media != "application/dns-message" {
		return nil, fmt.Errorf("invalid DoH response: HTTP %d", resp.StatusCode)
	}
	answer, err := io.ReadAll(io.LimitReader(resp.Body, maxDNSMessage+1))
	var a dnsmessage.Message
	if err != nil || len(answer) > maxDNSMessage || a.Unpack(answer) != nil || !a.Response || a.ID != q.ID || len(a.Questions) != len(q.Questions) {
		return nil, fmt.Errorf("invalid DNS response")
	}
	for i := range q.Questions {
		if a.Questions[i] != q.Questions[i] {
			return nil, fmt.Errorf("DNS question mismatch")
		}
	}
	return answer, nil
}
