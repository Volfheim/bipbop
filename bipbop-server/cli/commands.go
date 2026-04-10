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
				room = "https://telemost.yandex.ru/j/1234567890" // Пример
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
	ctx := context.Background()
	sess := &core.Session{}
	rch := make(chan struct{}, 1)

	key := core.EncodeSmartKey(listenAddr, password)

	ym, cl, err := core.Establish(nil, key, "Server", true)
	if err == nil {
		sess.Set(ym, cl)
	}

	go core.HealthLoop(ctx, sess, rch)
	go core.ReconnectLoop(ctx, sess, nil, key, "Server", true, rch)

	srv, _ := socks5.New(&socks5.Config{})

	for {
		y, ok := sess.Get()
		if !ok || y == nil {
			time.Sleep(1 * time.Second)
			continue
		}

		st, err := y.AcceptStream()
		if err != nil {
			time.Sleep(1 * time.Second)
			continue
		}
		go func() {
			fmt.Printf("[INFO] Accepted new stream\n")
			srv.ServeConn(st)
		}()
	}
}
