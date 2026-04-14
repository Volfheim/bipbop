package core

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// SignalingHosts - Кастомная мапа для обхода блокировок DNS
var SignalingHosts = map[string][]string{
	"cloud-api.yandex.ru":    {"87.250.250.242", "93.158.134.242"},
	"goloom.strm.yandex.net": {"87.250.254.244", "213.180.193.226", "93.158.134.254"},
	"stun.rtc.yandex.net":    {"87.250.250.119", "213.180.193.119"},
}

// SignalingDialer - Кастомный диалер, который пробует System DNS -> Static -> DoH.
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
	targets := []string{}
	if staticIPs, ok := SignalingHosts[host]; ok {
		targets = append(targets, staticIPs...)
	}

	// 3. Пытаемся DoH (Cloudflare) если хардкод не помог или для надежности
	dohIPs := ResolveDoH(host)
	targets = append(targets, dohIPs...)

	if len(targets) > 0 {
		fmt.Printf("[ANTI-JAM] DNS Bypass for %s triggered (Static/DoH targets count: %d)\n", host, len(targets))
		for _, ip := range targets {
			dialAddr := net.JoinHostPort(ip, port)
			conn, err := d.Dialer.DialContext(ctx, network, dialAddr)
			if err == nil {
				return conn, nil
			}
		}
	}

	// 4. Если ничего не помогло, возвращаем исходную ошибку
	return d.Dialer.DialContext(ctx, network, addr)
}

// ResolveDoH - Резолвинг через Cloudflare DoH (JSON API)
func ResolveDoH(host string) []string {
	client := &http.Client{Timeout: 5 * time.Second}
	url := fmt.Sprintf("https://1.1.1.1/dns-query?name=%s&type=A", host)
	
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("Accept", "application/dns-json")
	
	resp, err := client.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	
	var res struct {
		Answer []struct {
			Data string `json:"data"`
		} `json:"Answer"`
	}
	
	body, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(body, &res); err != nil {
		return nil
	}
	
	var ips []string
	for _, a := range res.Answer {
		if net.ParseIP(a.Data) != nil {
			ips = append(ips, a.Data)
		}
	}
	return ips
}
