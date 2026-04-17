package core

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

// SignalingHosts - Кастомная мапа для обхода блокировок DNS
var SignalingHosts = map[string][]string{
	"cloud-api.yandex.ru":    {"213.180.204.127", "87.250.250.242", "93.158.134.242"},
	"goloom.strm.yandex.net": {"87.250.254.244", "213.180.193.226", "93.158.134.254"},
	"stun.rtc.yandex.net":    {"213.180.205.180", "87.250.250.119", "213.180.193.119"},
	// SberJazz (SaluteJazz) IPs for bypassing censorship
	"bk.salutejazz.ru":   {"185.65.148.100", "178.248.239.103", "178.248.233.18", "178.248.233.15"},
	"api.salutejazz.ru":  {"185.65.148.100", "178.248.239.103", "178.248.233.18"},
	"ws.salutejazz.ru":   {"185.65.148.100", "178.248.239.103"},
	"jazz.sber.ru":       {"178.248.239.103", "185.65.148.100"},
}

// SignalingDialer - Кастомный диалер с поддержкой параллельного опроса IP (Happy Eyeballs)
type SignalingDialer struct {
	net.Dialer
}

func (d *SignalingDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return d.Dialer.DialContext(ctx, network, addr)
	}

	// 1. Пытаемся зарезолвить через системный DNS (с коротким таймаутом)
	// Если резолвинг успешен, используем стандартный Dial.
	resolveCtx, cancel := context.WithTimeout(ctx, 1500*time.Millisecond)
	ips, err := net.DefaultResolver.LookupIP(resolveCtx, "ip4", host)
	cancel()

	var targets []string
	if err == nil && len(ips) > 0 {
		for _, ip := range ips {
			targets = append(targets, net.JoinHostPort(ip.String(), port))
		}
	}

	// 2. Если DNS упал или нет ответа, пробуем хардкод
	if staticIPs, ok := SignalingHosts[host]; ok {
		for _, ip := range staticIPs {
			targets = append(targets, net.JoinHostPort(ip, port))
		}
	}

	if len(targets) == 0 {
		return d.Dialer.DialContext(ctx, network, addr)
	}

	// 3. Parallel Dialing (Happy Eyeballs style)
	// Мы запускаем попытки подключения параллельно с небольшим отступом.
	type result struct {
		conn net.Conn
		err  error
	}
	resCh := make(chan result, len(targets))
	
	innerCtx, innerCancel := context.WithCancel(ctx)
	defer innerCancel()

	var wg sync.WaitGroup
	
	fmt.Printf("[ANTI-JAM] Connecting to %s (Parallel Dial across %d targets)\n", host, len(targets))

	for i, target := range targets {
		wg.Add(1)
		go func(t string, delay time.Duration) {
			defer wg.Done()
			if delay > 0 {
				select {
				case <-innerCtx.Done():
					return
				case <-time.After(delay):
				}
			}

			conn, err := d.Dialer.DialContext(innerCtx, network, t)
			if err == nil {
				select {
				case resCh <- result{conn: conn}:
					innerCancel() // Останавливаем остальные попытки
					fmt.Printf("[ANTI-JAM] Parallel Dial success on: %s\n", t)
				case <-ctx.Done():
					conn.Close()
				default:
					conn.Close()
				}
				return
			}
			resCh <- result{err: err}
		}(target, time.Duration(i)*300*time.Millisecond) // Старт каждые 300мс
	}

	// Ждем первого успеха или пока всё не упадет
	go func() {
		wg.Wait()
		close(resCh)
	}()

	var lastErr error
	for res := range resCh {
		if res.conn != nil {
			return res.conn, nil
		}
		if res.err != nil {
			lastErr = res.err
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, fmt.Errorf("all parallel dials failed for %s", host)
}
