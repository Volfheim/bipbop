package cli

import (
	"context"

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
		Short: "Generate a new remote Smart-Key and Room",
		Run: func(cmd *cobra.Command, args []string) {
			ctx := context.Background()
			roomInfo, err := core.CreateRoom(ctx)
			if err != nil {
				fmt.Printf("FATAL: Failed to create Jazz room: %v\n", err)
				return
			}
			
			key := core.EncodeSmartKey(roomInfo.RoomID, roomInfo.Password)
			fmt.Printf("VOLFHEIM_ROOM_URL=%s\n", roomInfo.RoomID)
			fmt.Printf("VOLFHEIM_PASSWORD=%s\n", roomInfo.Password)
			fmt.Printf("SMART_KEY=%s\n", key)
		},
	}
	return cmd
}

func runServer(cmd *cobra.Command, args []string) {
	fmt.Printf("[INFO] Volfheim Server %s starting...\n", core.Version)
	ctx := context.Background()
	sess := &core.Session{}
	rch := make(chan struct{}, 1)

	// Автоматическое создание комнаты, если не передана ссылка
	if listenAddr == "" {
		fmt.Printf("[INFO] Creating new SberJazz room...\n")
		roomInfo, err := core.CreateRoom(ctx)
		if err != nil {
			fmt.Printf("[FATAL] Failed to create Jazz room: %v\n", err)
			return
		}
		listenAddr = roomInfo.RoomID
		password = roomInfo.Password
		fmt.Printf("[INFO] Jazz room created successfully!\n")
	}

	key := core.EncodeSmartKey(listenAddr, password)

	mux, cl, err := core.Establish(nil, key, "Server", true)
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
					m.Reset()
				}
				bo := 2 * time.Second
				for a := 1; ; a++ {
					fmt.Printf("[INFO] Reconnecting (#%d)...\n", a)
					m, c, e := core.Establish(nil, key, "Server", true)
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

