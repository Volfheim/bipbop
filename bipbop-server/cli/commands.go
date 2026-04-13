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
	fmt.Printf("[INFO] Volfheim Server %s starting...\n", core.Version)
	ctx := context.Background()

	key := core.EncodeSmartKey(listenAddr, password)

	// Establish возвращает DCStream теперь (как в olcrtc)
	stream, cl, err := core.Establish(nil, key, "Server", true)
	if err != nil {
		fmt.Printf("[ERROR] Initial establish failed: %v\n", err)
		return
	}
	defer cl.Close()

	fmt.Println("[INFO] Tunnel established, starting SOCKS5 server over DataChannel...")

	srv, _ := socks5.New(&socks5.Config{})

	// Сервер принимает SocksHandshake через DataChannel
	// В olcrtc это делается через мультиплексор, тут через yamux-like dc stream
	// Для простоты: запуск socks5 сервера на единственном connection
	_ = ctx
	_ = stream

	// Простой серверный цикл: обслуживать соединение напрямую через DataChannel
	for {
		fmt.Println("[INFO] Serving SOCKS5 over DataChannel...")
		if err := srv.ServeConn(stream); err != nil {
			fmt.Printf("[WARN] ServeConn error: %v, reconnecting...\n", err)
			// Reconnect
			time.Sleep(3 * time.Second)
			stream, cl, err = core.Establish(nil, key, "Server", true)
			if err != nil {
				fmt.Printf("[ERROR] Reconnect failed: %v\n", err)
				time.Sleep(5 * time.Second)
				continue
			}
			fmt.Println("[INFO] Reconnected!")
		}
	}
}
