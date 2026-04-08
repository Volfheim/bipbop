package cli

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"sync"

	"github.com/armon/go-socks5"
	"github.com/hashicorp/yamux"
	"github.com/spf13/cobra"
	"github.com/xtaci/kcp-go/v5"

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
	cmd.Flags().StringVarP(&listenAddr, "listen", "l", "0.0.0.0:8443", "Listen address (ip:port)")
	cmd.Flags().StringVarP(&password, "password", "p", "", "Secret password (will generate if empty)")
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
			ip := "94.232.44.122" // Default or fetched IP
			key := core.EncodeSmartKey(ip, "8443", password)
			fmt.Printf("\n\033[33m┌─── SMART KEY [%s] ──────────────────────────────────────────┐\033[0m\n", ip)
			fmt.Printf("\033[33m│\033[0m \033[32m%s\033[0m\n", key)
			fmt.Printf("\033[33m└─────────────────────────────────────────────────────────────┘\033[0m\n\n")
		},
	}
	return cmd
}

func runServer(cmd *cobra.Command, args []string) {
	if password == "" {
		b := make([]byte, 16)
		rand.Read(b)
		password = hex.EncodeToString(b)
		fmt.Printf("[INFO] Auto-generated server password: %s\n", password)
	}

	blk, _ := kcp.NewAESBlockCrypt(core.DeriveKey(password))
	l, err := kcp.ListenWithOptions(listenAddr, blk, 10, 3)
	if err != nil {
		fmt.Printf("[FATAL] KCP Listen: %v\n", err)
		return
	}

	fmt.Printf("[INFO] Volfheim Server listening on \033[32m%s\033[0m\n", listenAddr)

	srv, _ := socks5.New(&socks5.Config{})
	var wg sync.WaitGroup

	for {
		s, err := l.AcceptKCP()
		if err != nil {
			continue
		}
		s.SetNoDelay(1, 10, 2, 1)
		s.SetWindowSize(1024, 1024)
		s.SetStreamMode(true)

		wg.Add(1)
		go func(s *kcp.UDPSession) {
			defer wg.Done()
			defer s.Close()
			ym, err := yamux.Server(s, core.YmxCfg())
			if err != nil {
				return
			}
			defer ym.Close()
			fmt.Printf("[INFO] Connected ← \033[33m%s\033[0m\n", s.RemoteAddr())
			for {
				st, err := ym.AcceptStream()
				if err != nil {
					fmt.Printf("[INFO] Disconnected ✕ \033[33m%s\033[0m\n", s.RemoteAddr())
					return
				}
				go srv.ServeConn(st)
			}
		}(s)
	}
}
