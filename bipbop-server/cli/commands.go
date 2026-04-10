package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

			// Пытаемся автоматически определить публичный IP сервера
			vpsIP := "YOUR_VPS_IP"
			resp, err := http.Get("https://api.ipify.org")
			if err == nil {
				defer resp.Body.Close()
				ipBytes, _ := io.ReadAll(resp.Body)
				if len(ipBytes) > 0 {
					vpsIP = string(ipBytes)
				}
			}

			key := core.EncodeSmartKey(room, password, vpsIP)
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

	// --- Signaling Proxy ---
	go func() {
		http.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
			room := r.URL.Query().Get("url")
			if room == "" {
				room = listenAddr
			}
			info, err := core.GetConnectionInfo(room, "VolfheimServer")
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(info)
		})
		fmt.Printf("[INFO] Signaling Proxy running on :80\n")
		http.ListenAndServe(":80", nil)
	}()

	key := core.EncodeSmartKey(listenAddr, password, "")

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
