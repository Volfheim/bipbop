package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/volfheim/bipbop/core"
)

type vpnEngine struct {
	ctx    context.Context
	cancel context.CancelFunc
	peer   string
	tunFd  int
	mtu    int
	dns    string
	status string

	sess      core.Session
	connCount atomic.Int64
	txBytes   atomic.Int64
	rxBytes   atomic.Int64
	dnsSem    chan struct{}
}

func (e *vpnEngine) run() error {
	socksAddr := "127.0.0.1:13349"

	// 1. Start SOCKS5 once
	socksLn, err := net.Listen("tcp", socksAddr)
	if err != nil {
		emit("error")
		return err
	}
	defer socksLn.Close()
	go e.serveSocks5(socksLn)

	// 2. First-time Establishment
	logToApp("info", "[ENG] Establishing initial tunnel...")
	mux, cl, err := core.Establish(nil, e.peer, "Guest", false)
	if err != nil {
		logToApp("error", fmt.Sprintf("[ENG] Initial establish failed: %v", err))
		emit("error")
		return err
	}
	e.sess.Set(mux, cl)
	emit("connected")
	logToApp("info", "[ENG] Initial tunnel established!")

	// 3. Start tun2socks only if tunFd != -1
	if e.tunFd != -1 {
		t2s, err := newTun2Socks(e.tunFd, socksAddr, e.mtu, e.dns)
		if err != nil {
			emit("error")
			return err
		}
		defer t2s.Close()
	}

	// 4. Main Reconnect Loop
	for {
		select {
		case <-e.ctx.Done():
			logToApp("warn", "[LIB] run() loop exiting due to context cancel (Stop called)")
			return nil
		case <-e.sess.Wait():
			logToApp("warn", "[ENG] Session wait triggered (Connection lost)")
			logToApp("warn", "[ENG] Redialing...")
			emit("reconnecting")

			for {
				m, cl, err := core.Establish(nil, e.peer, "Guest", false)
				if err != nil {
					logToApp("error", fmt.Sprintf("[ENG] Redial failed: %v, retrying...", err))
					select {
					case <-e.ctx.Done():
						return nil
					case <-time.After(5 * time.Second):
						continue
					}
				}
				e.sess.Set(m, cl)
				emit("connected")
				logToApp("info", "[ENG] Re-established!")
				break
			}
		}
	}
}

func (e *vpnEngine) stop() {
	e.cancel()
	e.sess.Stop()
}

func (e *vpnEngine) serveSocks5(ln net.Listener) {
	go func() { <-e.ctx.Done(); ln.Close() }()
	for {
		conn, err := ln.Accept()
		if err != nil {
			select {
			case <-e.ctx.Done():
				return
			default:
				continue
			}
		}
		go e.handleSocks5(conn)
	}
}

func (e *vpnEngine) handleSocks5(c net.Conn) {
	logToApp("info", "[ENG] New SOCKS5 connection handling started")
	defer c.Close()
	c.SetDeadline(time.Now().Add(60 * time.Second))

	mux, ok := e.sess.Get()
	if !ok || mux == nil {
		for i := 0; i < 25; i++ {
			select {
			case <-e.ctx.Done():
				return
			case <-time.After(200 * time.Millisecond):
				mux, ok = e.sess.Get()
			}
			if ok && mux != nil {
				break
			}
		}
	}

	if !ok || mux == nil {
		c.Write([]byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	// Открываем стрим через мультиплексор olcrtc
	sid := mux.OpenStream()
	s := core.NewMuxConn(sid, mux)
	defer s.Close()

	// Handshake
	buf := make([]byte, 258)
	n, err := c.Read(buf)
	if err != nil || n < 2 || buf[0] != 0x05 {
		return
	}
	c.Write([]byte{0x05, 0x00})

	n, err = c.Read(buf)
	if err != nil || n < 7 {
		return
	}

	cmd := buf[1]
	dstAddr, dstPort := parseSocks5Addr(buf[3:n])

	if cmd == 0x03 {
		e.handleUDPAssociate(c, dstAddr, dstPort)
		return
	}

	if cmd != 0x01 {
		c.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	// ConnectReq to remote via Mux
	s.Write([]byte{0x05, 0x01, 0x00})
	vBuf := make([]byte, 2)
	s.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(s, vBuf); err != nil || vBuf[0] != 0x05 || vBuf[1] != 0x00 {
		c.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	s.Write(buf[:n])
	respBuf := make([]byte, 256)
	s.SetReadDeadline(time.Now().Add(10 * time.Second))
	rn, err := s.Read(respBuf)
	if err != nil || rn < 2 {
		c.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	c.Write(respBuf[:rn])
	if respBuf[1] != 0x00 {
		return
	}

	s.SetDeadline(time.Time{})
	c.SetDeadline(time.Time{})

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		n, _ := io.Copy(s, c)
		e.txBytes.Add(n)
	}()
	go func() {
		defer wg.Done()
		n, _ := io.Copy(c, s)
		e.rxBytes.Add(n)
	}()
	wg.Wait()
}

func (e *vpnEngine) handleUDPAssociate(tcpConn net.Conn, clientAddr string, clientPort int) {
	udpAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
	if err != nil {
		tcpConn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	udpConn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		tcpConn.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer udpConn.Close()

	localAddr := udpConn.LocalAddr().(*net.UDPAddr)
	reply := []byte{0x05, 0x00, 0x00, 0x01}
	reply = append(reply, localAddr.IP.To4()...)
	reply = append(reply, byte(localAddr.Port>>8), byte(localAddr.Port))
	tcpConn.Write(reply)

	logToApp("info", fmt.Sprintf("[UDP-ASSOC] listening on %s", localAddr))

	done := make(chan struct{})
	go func() {
		buf := make([]byte, 1)
		tcpConn.Read(buf)
		close(done)
	}()

	dataBuf := make([]byte, 65536)
	for {
		select {
		case <-done:
			return
		case <-e.ctx.Done():
			return
		default:
		}
		udpConn.SetReadDeadline(time.Now().Add(5 * time.Second))
		n, senderAddr, err := udpConn.ReadFromUDP(dataBuf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}
		if n < 10 {
			continue
		}
		frag := dataBuf[2]
		if frag != 0 {
			continue
		}
		hdrLen, dstIP, dstPort := parseUDPHeader(dataBuf[3:n])
		if hdrLen == 0 {
			continue
		}
		dnsQuery := dataBuf[3+hdrLen : n]

		e.dnsSem <- struct{}{}
		go func(query []byte, dst string, dstP int, sender *net.UDPAddr) {
			defer func() { <-e.dnsSem }()
			resp, err := e.dnsOverTCPviaTunnel(query, dst, dstP)
			if err != nil {
				return
			}
			ip := net.ParseIP(dst).To4()
			if ip == nil {
				ip = net.IPv4(0, 0, 0, 0)
			}
			udpResp := []byte{0x00, 0x00, 0x00, 0x01}
			udpResp = append(udpResp, ip...)
			udpResp = append(udpResp, byte(dstP>>8), byte(dstP))
			udpResp = append(udpResp, resp...)
			udpConn.WriteToUDP(udpResp, sender)
		}(append([]byte(nil), dnsQuery...), dstIP, dstPort, senderAddr)
	}
}

func (e *vpnEngine) dnsOverTCPviaTunnel(query []byte, dstIP string, dstPort int) ([]byte, error) {
	mux, ok := e.sess.Get()
	if !ok || mux == nil {
		for i := 0; i < 25; i++ {
			select {
			case <-e.ctx.Done():
			case <-time.After(200 * time.Millisecond):
				mux, ok = e.sess.Get()
			}
			if ok && mux != nil {
				break
			}
		}
	}

	if !ok || mux == nil {
		return nil, fmt.Errorf("no session")
	}

	sid := mux.OpenStream()
	s := core.NewMuxConn(sid, mux)
	defer s.Close()

	s.SetDeadline(time.Now().Add(10 * time.Second))

	s.Write([]byte{0x05, 0x01, 0x00})
	hdr := make([]byte, 2)
	if _, err := io.ReadFull(s, hdr); err != nil || hdr[0] != 0x05 {
		return nil, fmt.Errorf("vps greeting: %v", err)
	}

	ip := net.ParseIP(dstIP).To4()
	if ip == nil {
		return nil, fmt.Errorf("bad ip: %s", dstIP)
	}
	connectReq := []byte{0x05, 0x01, 0x00, 0x01}
	connectReq = append(connectReq, ip...)
	connectReq = append(connectReq, byte(dstPort>>8), byte(dstPort))
	s.Write(connectReq)

	resp := make([]byte, 10)
	if _, err := io.ReadFull(s, resp); err != nil {
		return nil, fmt.Errorf("vps connect: %v", err)
	}
	if resp[1] != 0x00 {
		return nil, fmt.Errorf("vps rejected: status=%d", resp[1])
	}

	tcpBuf := make([]byte, 2+len(query))
	tcpBuf[0] = byte(len(query) >> 8)
	tcpBuf[1] = byte(len(query))
	copy(tcpBuf[2:], query)
	if _, err := s.Write(tcpBuf); err != nil {
		return nil, fmt.Errorf("dns write: %v", err)
	}

	lenBuf := make([]byte, 2)
	if _, err := io.ReadFull(s, lenBuf); err != nil {
		return nil, fmt.Errorf("dns read len: %v", err)
	}
	respLen := int(lenBuf[0])<<8 | int(lenBuf[1])
	if respLen > 4096 {
		return nil, fmt.Errorf("dns resp too large: %d", respLen)
	}
	dnsResp := make([]byte, respLen)
	if _, err := io.ReadFull(s, dnsResp); err != nil {
		return nil, fmt.Errorf("dns read body: %v", err)
	}
	return dnsResp, nil
}

func parseSocks5Addr(b []byte) (string, int) {
	if len(b) < 2 {
		return "", 0
	}
	switch b[0] {
	case 0x01:
		if len(b) < 7 {
			return "", 0
		}
		return net.IPv4(b[1], b[2], b[3], b[4]).String(), int(b[5])<<8 | int(b[6])
	case 0x03:
		dLen := int(b[1])
		if len(b) < 2+dLen+2 {
			return "", 0
		}
		return string(b[2 : 2+dLen]), int(b[2+dLen])<<8 | int(b[2+dLen+1])
	case 0x04:
		if len(b) < 19 {
			return "", 0
		}
		return net.IP(b[1:17]).String(), int(b[17])<<8 | int(b[18])
	}
	return "", 0
}

func parseUDPHeader(b []byte) (int, string, int) {
	if len(b) < 4 {
		return 0, "", 0
	}
	switch b[0] {
	case 0x01:
		if len(b) < 7 {
			return 0, "", 0
		}
		return 7, fmt.Sprintf("%d.%d.%d.%d", b[1], b[2], b[3], b[4]), int(b[5])<<8 | int(b[6])
	case 0x03:
		dLen := int(b[1])
		if len(b) < 2+dLen+2 {
			return 0, "", 0
		}
		return 2 + dLen + 2, string(b[2 : 2+dLen]), int(b[2+dLen])<<8 | int(b[2+dLen+1])
	case 0x04:
		if len(b) < 19 {
			return 0, "", 0
		}
		return 19, net.IP(b[1:17]).String(), int(b[17])<<8 | int(b[18])
	}
	return 0, "", 0
}
