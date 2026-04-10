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
	pw     string
	tunFd  int
	mtu    int
	dns    string
	status string

	sess      core.Session
	connCount atomic.Int64
	txBytes   atomic.Int64
	rxBytes   atomic.Int64
	dnsSem    chan struct{} // STABILITY FIX: Limit concurrent DNS queries to prevent OOM
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

	// 2. Start Health Check (triggers sess.Down -> sess.Wait)
	go func() {
		tk := time.NewTicker(core.HealthEvery)
		defer tk.Stop()
		for {
			select {
			case <-e.ctx.Done():
				return
			case <-tk.C:
				if y, ok := e.sess.Get(); ok && y != nil {
					if _, err := y.Ping(); err != nil {
						logToApp("warn", "[ENG] Link health check failed")
						e.sess.Down()
					}
				}
			}
		}
	}()

	// 3. Start tun2socks first (VPN Slot mode)
	if e.tunFd != -1 {
		t2s, err := newTun2Socks(e.tunFd, socksAddr, e.mtu, e.dns)
		if err != nil {
			emit("error")
			return err
		}
		defer t2s.Close()
	} else {
		logToApp("info", "[ENG] Обычный SOCKS5 режим (без захвата VPN-слота)")
	}

	// 4. Main Reconnect Loop
	for {
		select {
		case <-e.ctx.Done():
			return nil
		default:
		}

		logToApp("info", "[ENG] Establishing tunnel...")
		ym, cl, err := core.Establish(e.peer, e.pw, false)
		if err != nil {
			logToApp("error", fmt.Sprintf("[ENG] Establish failed: %v", err))
			emit("reconnecting")
			select {
			case <-e.ctx.Done():
				return nil
			case <-time.After(5 * time.Second):
				continue
			}
		}

		e.sess.Set(ym, cl)
		emit("connected")
		logToApp("info", "[ENG] Tunnel established!")

		// 5. Wait for failure or stop - BUT DON'T CLOSE T2S
		select {
		case <-e.ctx.Done():
			return nil
		case <-e.sess.Wait():
			logToApp("warn", "[ENG] Connection lost - redialing...")
			emit("reconnecting")
			// loop continues to Establish, keeping T2S alive!
		}
	}
}

func (e *vpnEngine) stop() {
	logToApp("info", "[ENG] stop() requested")
	e.cancel()
	e.sess.Stop()
}

// --- SOCKS5 proxy (for tun2socks) ---

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
	defer c.Close()
	c.SetDeadline(time.Now().Add(60 * time.Second))

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

	switch cmd {
	case 0x01:
		e.handleConnect(c, buf[:n])
	case 0x03:
		e.handleUDPAssociate(c, dstAddr, dstPort)
	default:
		c.Write([]byte{0x05, 0x07, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	}
}

func (e *vpnEngine) handleConnect(c net.Conn, connectReq []byte) {
	connID := e.connCount.Add(1)
	
	y, ok := e.sess.Get()
	if !ok || y == nil {
		// SMART PROXY FIX: Retry for a few seconds if session is transitioning (WiFi -> LTE)
		for i := 0; i < 25; i++ {
			select {
			case <-e.ctx.Done():
			case <-time.After(200 * time.Millisecond):
				y, ok = e.sess.Get()
			}
			if ok && y != nil {
				break
			}
		}
	}

	if !ok || y == nil {
		c.Write([]byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	s, err := y.OpenStream()
	if err != nil {
		logToApp("warn", fmt.Sprintf("[CONN#%d] yamux open: %v", connID, err))
		e.sess.Down()
		c.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer s.Close()
	c.SetDeadline(time.Time{})

	// SOCKS5 greeting to remote
	s.Write([]byte{0x05, 0x01, 0x00})
	vBuf := make([]byte, 2)
	s.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadFull(s, vBuf); err != nil || vBuf[0] != 0x05 || vBuf[1] != 0x00 {
		c.Write([]byte{0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	s.Write(connectReq)
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
		
		// STABILITY FIX: Use semaphore to limit goroutines and prevent OOM
		e.dnsSem <- struct{}{}
		go func(query []byte, dst string, dstP int, sender *net.UDPAddr) {
			defer func() { <-e.dnsSem }()
			resp, err := e.dnsOverTCPviaSocks5(query, dst, dstP)
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

func (e *vpnEngine) dnsOverTCPviaSocks5(query []byte, dstIP string, dstPort int) ([]byte, error) {
	y, ok := e.sess.Get()
	if !ok || y == nil {
		// SMART PROXY FIX: Retry for a few seconds if session is transitioning
		for i := 0; i < 25; i++ {
			select {
			case <-e.ctx.Done():
			case <-time.After(200 * time.Millisecond):
				y, ok = e.sess.Get()
			}
			if ok && y != nil {
				break
			}
		}
	}

	if !ok || y == nil {
		return nil, fmt.Errorf("no session")
	}
	s, err := y.OpenStream()
	if err != nil {
		return nil, fmt.Errorf("yamux: %v", err)
	}
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

// --- SOCKS5 address parsers ---

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
