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

	result := make(chan net.Conn, 1)
	errs := make(chan error, len(staticIPs)+1)
	fastCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// 1. Попытка через системный DNS (в фоне)
	go func() {
		conn, err := d.Dialer.DialContext(fastCtx, network, addr)
		if err == nil {
			select {
			case result <- conn:
			default:
				conn.Close()
			}
		} else {
			errs <- err
		}
	}()

	// 2. Попытки через хардкод IP (параллельно, но с задержкой 200мс)
	for _, ip := range staticIPs {
		go func(targetIP string) {
			time.Sleep(200 * time.Millisecond)
			dialAddr := net.JoinHostPort(targetIP, port)
			conn, err := d.Dialer.DialContext(fastCtx, network, dialAddr)
			if err == nil {
				select {
				case result <- conn:
					fmt.Printf("[ANTI-JAM] Parallel dial WINNER: %s (static)\n", targetIP)
				default:
					conn.Close()
				}
			} else {
				errs <- err
			}
		}(ip)
	}

	// Ждем первого успеха или пока всё не упадет
	totalAttempts := len(staticIPs) + 1
	for i := 0; i < totalAttempts; i++ {
		select {
		case conn := <-result:
			return conn, nil
		case <-errs:
			// продолжаем ждать
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	return nil, fmt.Errorf("all dialing attempts failed for %s", addr)
}
