package core

import (
	"context"
	"fmt"
	"net"
	"time"
)

// SignalingHosts - Кастомная мапа для обхода блокировок DNS
var SignalingHosts = map[string][]string{
	"cloud-api.yandex.ru":    {"213.180.204.127", "213.180.204.127", "87.250.250.242", "93.158.134.242"},
	"goloom.strm.yandex.net": {"87.250.254.244", "213.180.193.226", "93.158.134.254"},
	"stun.rtc.yandex.net":    {"213.180.205.180", "213.180.205.180", "87.250.250.119", "213.180.193.119"},
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

	// 1. Пытаемся зарезолвить через системный DNS (с коротким таймаутом)
	resolveCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	ips, err := net.DefaultResolver.LookupIP(resolveCtx, "ip4", host)
	cancel()

	if err == nil && len(ips) > 0 {
		return d.Dialer.DialContext(ctx, network, addr)
	}

	// 2. Если DNS упал (i/o timeout), пробуем хардкод
	if staticIPs, ok := SignalingHosts[host]; ok {
		fmt.Printf("[ANTI-JAM] DNS Bypass for %s triggered (using static IPs)\n", host)
		for _, ip := range staticIPs {
			dialAddr := net.JoinHostPort(ip, port)
			conn, err := d.Dialer.DialContext(ctx, network, dialAddr)
			if err == nil {
				return conn, nil
			}
		}
	}

	// 3. Если ничего не помогло, возвращаем исходную ошибку
	return d.Dialer.DialContext(ctx, network, addr)
}
