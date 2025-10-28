package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var (
	// Chat flags
	chatRouterAddr string
	chatDataDir    string
	chatGenKey     bool
)

var rootCmd = &cobra.Command{
	Use:   "sendy",
	Short: "Sendy - P2P encrypted chat application",
	Long: `Sendy is a peer-to-peer encrypted chat application with WebRTC connections.

By default, running 'sendy' starts the chat client.
Use 'sendy router' to start the router server.`,
	Run: runChat,
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	// Add chat flags to root command
	rootCmd.Flags().StringVarP(&chatRouterAddr, "router", "r", "localhost:9090", "Router server address")
	rootCmd.Flags().StringVarP(&chatDataDir, "data", "d", "", "Base directory (default: ~/.sendy)")
	rootCmd.Flags().BoolVarP(&chatGenKey, "genkey", "g", false, "Generate new keypair and exit")

	rootCmd.CompletionOptions.DisableDefaultCmd = true
}

func exitWithError(msg string, err error) {
	fmt.Fprintf(os.Stderr, "❌ %s: %v\n", msg, err)
	os.Exit(1)
}
