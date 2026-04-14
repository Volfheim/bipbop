package core

import (
	"context"
	"fmt"
	"net"
	"time"
)

// SignalingHosts - Кастомная мапа для обхода блокировок DNS
var SignalingHosts = map[string][]string{
	"cloud-api.yandex.ru":    {"213.180.204.127", "213.180.193.127"},
	"goloom.strm.yandex.net": {"213.180.204.244", "213.180.193.226"},
	"stun.rtc.yandex.net":    {"213.180.205.180", "213.180.193.119"},
}

// SignalingDialer - Кастомный диалер, который сначала пробует системный DNS,
// а при таймауте (глушилке) — переключается на хардкод.
type SignalingDialer struct {
	net.Dialer
}

func (d *SignalingDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return d.Dialer.DialContext(ctx, network, addr)
	}

	staticIPs, isSignaling := SignalingHosts[host]
	if !isSignaling {
		return d.Dialer.DialContext(ctx, network, addr)
	}

	// 1. Пытаемся через системный DNS (максимум 4 секунды)
	dnsCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	conn, err := d.Dialer.DialContext(dnsCtx, network, addr)
	cancel()
	if err == nil {
		return conn, nil
	}

	// 2. Если DNS молчит (глушилка), пробуем статические IP по очереди
	fmt.Printf("[ANTI-JAM] DNS Bypass for %s\n", host)
	for _, ip := range staticIPs {
		dialAddr := net.JoinHostPort(ip, port)
		staticCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
		c, err := d.Dialer.DialContext(staticCtx, network, dialAddr)
		cancel()
		if err == nil {
			fmt.Printf("[ANTI-JAM] Connected via static IP: %s\n", ip)
			return c, nil
		}
	}

	return nil, fmt.Errorf("all connection attempts for %s failed", addr)
}
