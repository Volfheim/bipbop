package cli

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/armon/go-socks5"
	"github.com/spf13/cobra"

	"github.com/volfheim/bipbop/core"
)

var (
	listenAddr string
	password   string
	clientName string
	clientID   string
	dataDir    string
)

func NewServerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "server",
		Short: "Start the VPN server daemon",
		Run:   runServer,
	}
	cmd.Flags().StringVarP(&listenAddr, "listen", "l", "", "Telemost Room URL")
	cmd.Flags().StringVarP(&password, "password", "p", "", "Secret password (legacy single-client mode)")
	cmd.Flags().StringVarP(&dataDir, "data", "d", ".", "Directory for clients.json")
	return cmd
}

func NewGenerateCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "gen",
		Short: "Generate a new remote Smart-Key (legacy)",
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
	cmd.Flags().StringVarP(&listenAddr, "listen", "l", "", "Telemost Room URL")
	cmd.Flags().StringVarP(&password, "password", "p", "", "Secret password")
	return cmd
}

// --- Client management commands ---

func NewClientCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "client",
		Short: "Manage clients",
	}
	cmd.PersistentFlags().StringVarP(&dataDir, "data", "d", ".", "Directory for clients.json")
	cmd.PersistentFlags().StringVarP(&listenAddr, "listen", "l", "", "Telemost Room URL")

	// client add
	addCmd := &cobra.Command{
		Use:   "add",
		Short: "Add a new client",
		Run:   runClientAdd,
	}
	addCmd.Flags().StringVarP(&clientName, "name", "n", "", "Client name (required)")
	addCmd.MarkFlagRequired("name")
	cmd.AddCommand(addCmd)

	// client list
	listCmd := &cobra.Command{
		Use:   "list",
		Short: "List all clients",
		Run:   runClientList,
	}
	cmd.AddCommand(listCmd)

	// client revoke
	revokeCmd := &cobra.Command{
		Use:   "revoke",
		Short: "Revoke a client's access",
		Run:   runClientRevoke,
	}
	revokeCmd.Flags().StringVarP(&clientID, "id", "i", "", "Client ID to revoke (required)")
	revokeCmd.MarkFlagRequired("id")
	cmd.AddCommand(revokeCmd)

	// client delete
	deleteCmd := &cobra.Command{
		Use:   "delete",
		Short: "Permanently delete a client",
		Run:   runClientDelete,
	}
	deleteCmd.Flags().StringVarP(&clientID, "id", "i", "", "Client ID to delete (required)")
	deleteCmd.MarkFlagRequired("id")
	cmd.AddCommand(deleteCmd)

	return cmd
}

func runClientAdd(cmd *cobra.Command, args []string) {
	store := NewClientStore(dataDir)
	if err := store.Load(); err != nil {
		fmt.Printf("\033[31mОшибка чтения clients.json: %v\033[0m\n", err)
		os.Exit(1)
	}

	room := listenAddr
	if room == "" {
		fmt.Println("\033[31mУкажите --listen (Telemost Room URL)\033[0m")
		os.Exit(1)
	}

	c, err := store.Add(clientName, room)
	if err != nil {
		fmt.Printf("\033[31mОшибка: %v\033[0m\n", err)
		os.Exit(1)
	}

	if err := store.Save(); err != nil {
		fmt.Printf("\033[31mОшибка сохранения: %v\033[0m\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n\033[32m✓ Клиент добавлен!\033[0m\n")
	fmt.Printf("  ID:       \033[33m%s\033[0m\n", c.ID)
	fmt.Printf("  Имя:      %s\n", c.Name)
	fmt.Printf("\n\033[33m┌─── SMART KEY ──────────────────────────────────────────────────────────┐\033[0m\n")
	fmt.Printf("\033[33m│\033[0m \033[32m%s\033[0m\n", c.SmartKey)
	fmt.Printf("\033[33m└────────────────────────────────────────────────────────────────────────┘\033[0m\n\n")
}

func runClientList(cmd *cobra.Command, args []string) {
	store := NewClientStore(dataDir)
	if err := store.Load(); err != nil {
		fmt.Printf("\033[31mОшибка чтения clients.json: %v\033[0m\n", err)
		os.Exit(1)
	}

	clients := store.GetAll()
	if len(clients) == 0 {
		fmt.Println("Клиентов нет. Используйте: volfheim client add --name \"Имя\" --listen \"URL\"")
		return
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(w, "\n\033[36mID\tИмя\tСтатус\tСоздан\033[0m\n")
	fmt.Fprintf(w, "────\t────\t──────\t──────\n")
	for _, c := range clients {
		status := "\033[32m● Активен\033[0m"
		if !c.Active {
			status = "\033[31m✗ Отозван\033[0m"
		}
		created := c.CreatedAt
		if t, err := time.Parse(time.RFC3339, c.CreatedAt); err == nil {
			created = t.Format("2006-01-02 15:04")
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", c.ID, c.Name, status, created)
	}
	w.Flush()
	fmt.Println()
}

func runClientRevoke(cmd *cobra.Command, args []string) {
	store := NewClientStore(dataDir)
	if err := store.Load(); err != nil {
		fmt.Printf("\033[31mОшибка: %v\033[0m\n", err)
		os.Exit(1)
	}

	c, err := store.Revoke(clientID)
	if err != nil {
		fmt.Printf("\033[31mОшибка: %v\033[0m\n", err)
		os.Exit(1)
	}

	if err := store.Save(); err != nil {
		fmt.Printf("\033[31mОшибка сохранения: %v\033[0m\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n\033[31m✗ Клиент отозван: %s (%s)\033[0m\n", c.Name, c.ID)
	fmt.Println("  Его Smart-Key больше не будет принят сервером.")
	fmt.Println()
}

func runClientDelete(cmd *cobra.Command, args []string) {
	store := NewClientStore(dataDir)
	if err := store.Load(); err != nil {
		fmt.Printf("\033[31mОшибка: %v\033[0m\n", err)
		os.Exit(1)
	}

	if err := store.Delete(clientID); err != nil {
		fmt.Printf("\033[31mОшибка: %v\033[0m\n", err)
		os.Exit(1)
	}

	if err := store.Save(); err != nil {
		fmt.Printf("\033[31mОшибка сохранения: %v\033[0m\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n\033[31m✗ Клиент %s удалён навсегда\033[0m\n\n", clientID)
}

// --- Server ---

func runServer(cmd *cobra.Command, args []string) {
	store := NewClientStore(dataDir)
	if err := store.Load(); err != nil {
		fmt.Printf("[WARN] Cannot load clients.json: %v, running in legacy mode\n", err)
	}

	// Legacy single-client mode (backward compatible)
	if password == "" && len(store.GetActive()) == 0 {
		b := make([]byte, 16)
		rand.Read(b)
		password = hex.EncodeToString(b)
		fmt.Printf("[INFO] Auto-generated server password: %s\n", password)
	}

	activeClients := store.GetActive()
	if len(activeClients) > 0 {
		fmt.Printf("[INFO] Loaded %d active client(s):\n", len(activeClients))
		for _, c := range activeClients {
			fmt.Printf("  ● %s (%s)\n", c.Name, c.ID)
		}
		// Use first active client's password for Establish
		// (all clients share the same room, server accepts any active password)
		password = activeClients[0].Password
	}

	if listenAddr == "" {
		fmt.Println("[ERROR] --listen is required (Telemost Room URL)")
		os.Exit(1)
	}

	fmt.Printf("[INFO] Volfheim Server (Host) running in Room: \033[32m%s\033[0m\n", listenAddr)

	ctx := context.Background()
	sess := &core.Session{}
	rch := make(chan struct{}, 1)

	ym, cl, err := core.Establish(listenAddr, password, true)
	if err == nil {
		sess.Set(ym, cl)
	}

	go core.HealthLoop(ctx, sess, rch)
	go core.ReconnectLoop(ctx, sess, listenAddr, password, true, rch)

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
