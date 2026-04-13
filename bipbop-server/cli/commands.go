package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/armon/go-socks5"
	"github.com/spf13/cobra"

	"github.com/volfheim/bipbop/core"
)

var (
	listenAddr string
	password   string
)

func NewServerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Start the VPN server daemon",
		Run:   runServer,
	}
	cmd.Flags().StringVarP(&listenAddr, "listen", "l", "", "Telemost Room URL")
	cmd.Flags().StringVarP(&password, "password", "p", "", "Secret password")
	return cmd
}

func NewGenerateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gen",
		Short: "Generate a new remote Smart-Key",
		Run: func(cmd *cobra.Command, args []string) {
			if password == "" {
				b := make([]byte, 16)
				rand.Read(b)
				password = hex.EncodeToString(b)
			}
			room := listenAddr
			if room == "" {
				room = "https://telemost.yandex.ru/j/1234567890"
			}

			key := core.EncodeSmartKey(room, password)
			fmt.Printf("\n\033[33m┌─── SMART KEY ───────────────────────────────────────────────┐\033[0m\n")
			fmt.Printf("\033[33m│\033[0m \033[32m%s\033[0m\n", key)
			fmt.Printf("\033[33m└─────────────────────────────────────────────────────────────┘\033[0m\n\n")
		},
	}
	return cmd
}

func runServer(cmd *cobra.Command, args []string) {
	fmt.Printf("[INFO] Volfheim Server %s starting...\n", core.Version)
	ctx := context.Background()
	sess := &core.Session{}
	rch := make(chan struct{}, 1)

	key := core.EncodeSmartKey(listenAddr, password)

	mux, cl, err := core.Establish(nil, key, "Server", true, func() { sess.Down() })
	if err == nil {
		sess.Set(mux, cl)
	}

	// Health check (app-level heartbeats are inside webrtc.go, but we add a mux check)
	go func() {
		tk := time.NewTicker(core.HealthEvery)
		defer tk.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tk.C:
				if m, ok := sess.Get(); ok && m != nil {
					// В 4.2-PURE мы полагаемся на WebRTC heartbeats.
					// Если DataChannel закроется, Establish вернет ошибку или session упадет.
				}
			}
		}
	}()

	// Reconnect loop
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-rch:
				if m, ok := sess.Get(); ok && m != nil {
					m.Close()
				}
				bo := 2 * time.Second
				for a := 1; ; a++ {
					fmt.Printf("[INFO] Reconnecting (#%d)...\n", a)
					m, c, e := core.Establish(nil, key, "Server", true, func() { sess.Down() })
					if e == nil {
						sess.Set(m, c)
						fmt.Println("[INFO] Connection restored!")
						break
					}
					fmt.Printf("[WARN] Attempt %d failed: %v\n", a, e)
					time.Sleep(bo)
					if bo *= 2; bo > core.MaxBackoff {
						bo = core.MaxBackoff
					}
				}
			}
		}
	}()

	srv, _ := socks5.New(&socks5.Config{})

	for {
		m, ok := sess.Get()
		if !ok || m == nil {
			time.Sleep(1 * time.Second)
			continue
		}

		sid, err := m.AcceptStream()
		if err != nil {
			continue
		}
		
		go func(sID uint16, multiplexer *core.Multiplexer) {
			fmt.Printf("[INFO] Accepted new mux stream: %d\n", sID)
			conn := core.NewMuxConn(sID, multiplexer)
			defer conn.Close()
			if err := srv.ServeConn(conn); err != nil {
				fmt.Printf("[WARN] Mux stream %d error: %v\n", sID, err)
			}
		}(sid, m)
	}
}
