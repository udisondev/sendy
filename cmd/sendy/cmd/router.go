package cmd

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/udisondev/sendy/router"
)

var (
	routerAddr   string
	routerLogDir string
)

var routerCmd = &cobra.Command{
	Use:   "router",
	Short: "Start the router server",
	Long:  `Start the Sendy router server for WebRTC signaling and message routing.`,
	Run:   runRouter,
}

func init() {
	routerCmd.Flags().StringVarP(&routerAddr, "addr", "a", ":9090", "Server listen address")
	routerCmd.Flags().StringVarP(&routerLogDir, "logdir", "l", "logs", "Directory for log files")

	rootCmd.AddCommand(routerCmd)
}

func runRouter(cmd *cobra.Command, args []string) {
	// Determine base directory
	baseDir := routerLogDir
	if baseDir == "logs" {
		// Default: use ~/.sendy/logs/router/
		home, err := os.UserHomeDir()
		if err != nil {
			exitWithError("Cannot determine home directory", err)
		}
		baseDir = filepath.Join(home, ".sendy", "logs", "router")
	} else {
		// Custom logdir: use as-is but ensure router subdirectory
		baseDir = filepath.Join(baseDir, "router")
	}

	// Create log directory
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		exitWithError("Failed to create log directory", err)
	}

	// Create log file with timestamp
	logFileName := fmt.Sprintf("router-%s.log", time.Now().Format("2006-01-02_15-04-05"))
	logPath := filepath.Join(baseDir, logFileName)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		exitWithError("Failed to open log file", err)
	}
	defer logFile.Close()

	// Configure slog to write to file and stdout
	multiWriter := io.MultiWriter(os.Stdout, logFile)
	logLevel := slog.LevelInfo
	if os.Getenv("DEBUG") != "" {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(multiWriter, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(logger)

	slog.Info("Starting Sendy Router", "addr", routerAddr, "logfile", logPath)

	if err := router.Run(routerAddr); err != nil {
		slog.Error("Router error", "error", err)
		exitWithError("Router error", err)
	}
}
