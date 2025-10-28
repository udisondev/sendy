package cmd

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"sendy/chat"
	"sendy/p2p"
	"sendy/router"
)

func runChat(cmd *cobra.Command, args []string) {
	if chatGenKey {
		pubkey, privkey, _ := ed25519.GenerateKey(rand.Reader)
		fmt.Println("Public key (your ID):", hex.EncodeToString(pubkey))
		fmt.Println("Private key:", hex.EncodeToString(privkey.Seed()))
		fmt.Println("\nSave these keys securely!")
		return
	}

	// Determine base directory
	baseDir := chatDataDir
	if baseDir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			exitWithError("Cannot determine home directory", err)
		}
		baseDir = filepath.Join(home, ".sendy")
	}

	// Create directory structure
	logDir := filepath.Join(baseDir, "logs", "chat")
	dataDir := filepath.Join(baseDir, "data")

	if err := os.MkdirAll(logDir, 0755); err != nil {
		exitWithError("Cannot create log directory", err)
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		exitWithError("Cannot create data directory", err)
	}

	// Configure file logging
	logFileName := fmt.Sprintf("chat-%s.log", time.Now().Format("2006-01-02_15-04-05"))
	logPath := filepath.Join(logDir, logFileName)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		exitWithError("Failed to open log file", err)
	}
	defer logFile.Close()

	// Configure slog to write to file (stdout is used by TUI)
	logLevel := slog.LevelInfo
	if os.Getenv("DEBUG") != "" {
		logLevel = slog.LevelDebug
	}
	logger := slog.New(slog.NewTextHandler(logFile, &slog.HandlerOptions{
		Level: logLevel,
	}))
	slog.SetDefault(logger)

	slog.Info("Starting Sendy Chat", "baseDir", baseDir, "logfile", logPath)

	// File paths
	keyFile := filepath.Join(dataDir, "key")
	dbFile := filepath.Join(dataDir, "chat.db")

	// Load or generate keys
	pubkey, privkey, err := loadOrGenerateKeys(keyFile)
	if err != nil {
		slog.Error("Key management error", "error", err)
		exitWithError("Key management error", err)
	}

	myID := router.PeerID{}
	copy(myID[:], pubkey)

	hexID := hex.EncodeToString(myID[:])
	fmt.Printf("Your ID: %s\n", hexID)
	fmt.Printf("Connecting to router at %s...\n", chatRouterAddr)
	slog.Info("Loaded keys", "myID", hexID)

	// Create router client
	client := router.NewClient(pubkey, privkey)
	slog.Debug("Created router client")

	// Create context for application lifecycle
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Connect to router with connection timeout
	slog.Info("Connecting to router", "address", chatRouterAddr, "timeout", "10s")

	// Create channel for connection result
	type dialResult struct {
		income <-chan router.ServerMessage
		err    error
	}
	resultCh := make(chan dialResult, 1)

	go func() {
		income, err := client.Dial(ctx, chatRouterAddr)
		resultCh <- dialResult{income, err}
	}()

	// Wait for connection with timeout
	var income <-chan router.ServerMessage
	select {
	case result := <-resultCh:
		if result.err != nil {
			slog.Error("Failed to connect to router", "address", chatRouterAddr, "error", result.err)
			fmt.Fprintf(os.Stderr, "\n❌ Failed to connect to router at %s\n", chatRouterAddr)
			fmt.Fprintf(os.Stderr, "Error: %v\n\n", result.err)
			fmt.Fprintf(os.Stderr, "Make sure the router server is running:\n")
			fmt.Fprintf(os.Stderr, "  sendy router --addr %s\n\n", chatRouterAddr)
			os.Exit(1)
		}
		income = result.income
	case <-time.After(10 * time.Second):
		slog.Error("Connection timeout", "address", chatRouterAddr)
		fmt.Fprintf(os.Stderr, "\n❌ Connection timeout to router at %s\n", chatRouterAddr)
		fmt.Fprintf(os.Stderr, "Make sure the router server is running and accessible.\n\n")
		os.Exit(1)
	}

	fmt.Println("✓ Connected to router")
	slog.Info("Successfully connected to router")

	// Create P2P connector
	stunServers := getSTUNServers(chatSTUNServers)
	connectorCfg := p2p.ConnectorConfig{
		STUNServers: stunServers,
	}
	slog.Debug("Creating P2P connector with encryption", "stunServers", connectorCfg.STUNServers)
	connector, err := p2p.NewConnector(client, connectorCfg, income, privkey)
	if err != nil {
		slog.Error("Failed to create P2P connector", "error", err)
		log.Fatal("Failed to create P2P connector:", err)
	}
	fmt.Println("P2P connector initialized with end-to-end encryption")
	slog.Info("P2P connector initialized with encryption")

	// Create storage
	slog.Debug("Opening database", "path", dbFile)
	storage, err := chat.NewStorage(dbFile)
	if err != nil {
		slog.Error("Failed to open database", "path", dbFile, "error", err)
		exitWithError("Failed to open database", err)
	}
	defer storage.Close()
	fmt.Println("Database opened")
	slog.Info("Database opened", "path", dbFile)

	// Create chat
	slog.Debug("Creating chat instance")
	chatInstance := chat.NewChat(connector, storage, dataDir)
	defer chatInstance.Close()
	fmt.Println("Chat initialized")
	slog.Info("Chat initialized")

	fmt.Println("\nStarting TUI...")
	fmt.Println()
	slog.Info("Starting TUI")

	// Start TUI
	if err := chat.RunTUI(chatInstance, myID); err != nil {
		slog.Error("TUI error", "error", err)
		exitWithError("TUI error", err)
	}

	slog.Info("Chat exiting gracefully")
}

func loadOrGenerateKeys(keyFile string) (ed25519.PublicKey, ed25519.PrivateKey, error) {
	// Try to load existing keys
	slog.Debug("Attempting to load keys", "path", keyFile)
	data, err := os.ReadFile(keyFile)
	if err == nil {
		// File exists
		if len(data) != ed25519.PrivateKeySize {
			slog.Error("Invalid key file size", "path", keyFile, "size", len(data), "expected", ed25519.PrivateKeySize)
			return nil, nil, fmt.Errorf("invalid key file size")
		}

		privkey := ed25519.PrivateKey(data)
		pubkey := privkey.Public().(ed25519.PublicKey)

		fmt.Println("Loaded existing keys")
		slog.Info("Loaded existing keys from file", "path", keyFile)
		return pubkey, privkey, nil
	}

	// Generate new keys
	fmt.Println("Generating new keypair...")
	slog.Info("Generating new keypair", "reason", "key file not found")
	pubkey, privkey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		slog.Error("Failed to generate key", "error", err)
		return nil, nil, fmt.Errorf("generate key: %w", err)
	}

	// Save private key
	slog.Debug("Saving private key", "path", keyFile)
	if err := os.WriteFile(keyFile, privkey, 0600); err != nil {
		slog.Error("Failed to save key", "path", keyFile, "error", err)
		return nil, nil, fmt.Errorf("save key: %w", err)
	}

	fmt.Println("New keys generated and saved")
	slog.Info("New keys generated and saved", "path", keyFile)
	return pubkey, privkey, nil
}

// getSTUNServers returns STUN server list with priority:
// 1. From --stun-servers flag
// 2. From SENDY_STUN_SERVERS environment variable
// 3. Default verified servers (Google + Cloudflare + Twilio)
func getSTUNServers(flagValue string) []string {
	// Priority 1: command line flag
	if flagValue != "" {
		servers := strings.Split(flagValue, ",")
		// Trim whitespace
		for i := range servers {
			servers[i] = strings.TrimSpace(servers[i])
		}
		slog.Info("Using STUN servers from flag", "servers", servers)
		return servers
	}

	// Priority 2: environment variable
	if env := os.Getenv("SENDY_STUN_SERVERS"); env != "" {
		servers := strings.Split(env, ",")
		// Trim whitespace
		for i := range servers {
			servers[i] = strings.TrimSpace(servers[i])
		}
		slog.Info("Using STUN servers from environment", "servers", servers)
		return servers
	}

	// Priority 3: default verified servers
	// Only tested working servers
	defaultServers := []string{
		// Google (popular, reliable, ~0.15s)
		"stun:stun.l.google.com:19302",
		"stun:stun1.l.google.com:19302",

		// Cloudflare (fastest, ~0.05s)
		"stun:stun.cloudflare.com:3478",

		// Twilio (production-ready, ~0.17s)
		"stun:global.stun.twilio.com:3478",
	}
	slog.Debug("Using default STUN servers", "servers", defaultServers)
	return defaultServers
}
