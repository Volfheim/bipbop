package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/volfheim/bipbop-server/cli"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "volfheim",
		Short: "Volfheim VPN - Custom Server Control Plane",
		Long:  `Server software for Volfheim VPN to handle STUN/TURN tunneled clients.`,
	}

	rootCmd.AddCommand(cli.NewServerCmd())
	rootCmd.AddCommand(cli.NewGenerateCmd())

	if err := rootCmd.Execute(); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
}
