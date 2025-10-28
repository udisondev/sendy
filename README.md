# Sendy

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go Version](https://img.shields.io/badge/Go-1.21%2B-blue.svg)](https://golang.org/dl/)
[![Platform](https://img.shields.io/badge/Platform-Linux%20%7C%20macOS-lightgrey.svg)](https://github.com/yourusername/sendy)

**Sendy** is a peer-to-peer encrypted chat application with WebRTC connections and a terminal user interface (TUI). It provides end-to-end encryption for secure messaging and file transfers.

## Features

- 🔒 **End-to-End Encryption**: All messages and files encrypted with NaCl/box (Curve25519 + XSalsa20-Poly1305)
- 🔑 **Ed25519 Authentication**: Cryptographic identity for each peer
- 🌐 **P2P WebRTC Connections**: Direct peer-to-peer communication after initial handshake
- 💬 **Persistent Chat History**: SQLite database for messages and contacts
- 📁 **File Transfer**: Send files up to 200MB with encrypted transit
- 🎨 **Terminal UI**: Modern TUI built with Bubbletea
- 🚀 **High Performance**: 1.27 GB/s direct P2P transfer, 63,642 msg/s router throughput
- 📡 **NAT Traversal**: STUN servers for connecting peers behind NAT/firewalls
- 🚫 **Contact Blocking**: Block unwanted peers
- 📊 **Online Status**: Real-time connection status indicators

## Quick Start

### Prerequisites

- Go 1.21 or later
- `fzf` (for file picker): `brew install fzf` (macOS) or `sudo apt install fzf` (Ubuntu)

### Installation

```bash
# Clone the repository
git clone https://github.com/udisondev/sendy.git
cd sendy

# Build the unified CLI
go build -o bin/sendy ./cmd/sendy

# Or use Makefile
make build
```

### Running

**⚠️ IMPORTANT:** Start the router server first, then chat clients!

**Step 1: Start the Router Server**

```bash
./bin/sendy router
# Router starts on port 9090 by default
# Keep this terminal window open!
```

To use a different port:
```bash
./bin/sendy router --addr :7777
```

**Step 2: Start Chat Clients** (in new terminal windows)

```bash
# First client (default command)
./bin/sendy

# Or explicitly
./bin/sendy chat

# Second client (use different data directory)
./bin/sendy chat --data /tmp/alice

# If router is on different port
./bin/sendy chat --router localhost:7777
```

On first run, keys are automatically generated and your Peer ID is displayed.

**Step 3: Add Contacts and Chat**

1. Copy your Peer ID (64 hex characters) from the startup message
2. In the other client, press `a` to add contact
3. Paste the Peer ID and press Enter
4. Select the contact and press `c` to connect
5. Wait for `[Online]` status
6. Press `Tab` to switch to input field and type your message
7. Press `Enter` to send
8. Press `f` to send a file (opens fzf file picker)

## Usage

### Keyboard Shortcuts

**General:**
- `Tab` - Switch between panels (Contacts → Messages → Input)
- `q` - Quit (not available when focused on input field)

**Contact List Panel (left):**
- `↑/↓` or `j/k` - Navigate contacts
- `a` - Add new contact
- `i` - Show your Peer ID
- `d` - Delete contact and chat history
- `b` - Block/unblock contact
- `c` - Connect to selected contact
- `x` - Disconnect from selected contact

**Message Panel (top right):**
- `↑/↓` or `j/k` - Scroll messages
- `PgUp/PgDown` - Page through messages

**Input Panel (bottom right):**
- Type your message (multi-line supported)
- `Enter` - New line
- `Ctrl+S` - Send message
- `f` - Send file (opens fzf file picker)
- `Esc` - Cancel file selection

### Data Directory

By default, all data is stored in `~/.sendy/`:
```
~/.sendy/
├── logs/
│   ├── router/
│   │   └── router-*.log      # Router logs
│   └── chat/
│       └── chat-*.log        # Chat logs
└── data/
    ├── key                   # Ed25519 private key (protect this!)
    ├── chat.db               # SQLite database
    └── files/                # Received files
```

To use a custom directory:
```bash
./bin/sendy --data /path/to/data
```

### Generating New Keys

```bash
# Generate and display new keypair without starting chat
./sendy chat --genkey
```

## Architecture

```
┌──────────┐         ┌──────────┐
│  Alice   │         │   Bob    │
│ (Client) │         │ (Client) │
└────┬─────┘         └─────┬────┘
     │                     │
     │  WebRTC Signaling   │
     │    (encrypted)      │
     └────────┬────────────┘
              │
         ┌────▼─────┐
         │  Router  │
         │  Server  │
         └──────────┘
              │
     After connection:
              │
┌─────────────▼──────────────┐
│   Direct P2P Connection    │
│  (End-to-End Encrypted)    │
│    Alice ←─────→ Bob       │
└────────────────────────────┘
```

### Components

1. **Router Server** (`router/`)
   - Central signaling server for WebRTC handshake
   - Ed25519 authentication
   - Zero-copy I/O with `sync.Pool`
   - Does not see message content (only metadata)

2. **P2P Connector** (`p2p/`)
   - WebRTC peer-to-peer connections
   - Perfect negotiation for collision resolution
   - End-to-end encryption for all data
   - Contact blocklist management

3. **Chat** (`chat/`)
   - Message handling and persistence
   - Contact management
   - File transfer coordination
   - SQLite storage

4. **TUI** (`chat/tui.go`)
   - Terminal interface built with Bubbletea
   - Contact list with online status
   - Message history view
   - File picker integration (fzf)

## Security

Sendy implements end-to-end encryption for all peer-to-peer communications. For detailed security information, see [SECURITY.md](SECURITY.md).

### Highlights

- **Authentication**: Ed25519 digital signatures (256-bit security)
- **Encryption**: NaCl/box (Curve25519 + XSalsa20-Poly1305)
- **Trust Model**: TOFU (Trust On First Use)
- **Protection**: Messages, files, and WebRTC signaling are all encrypted

### Known Limitations

⚠️ **Please read before use:**
- No Perfect Forward Secrecy (PFS) - compromise of private key affects past messages
- MITM vulnerability on first connection (TOFU model)
- Router sees connection metadata (not content)
- No key rotation

See [SECURITY.md](SECURITY.md) for complete security documentation.

## Performance

**Router (Signaling):**
- 63,642 messages/sec (2 peers)
- 17.73 ns/op
- 24 B/op, 1 allocs/op

**WebRTC P2P Transfer:**
- 1.27 GB/s direct transfer
- 803.7 ns/op
- No server involvement after connection

## Configuration

### Router Server

```bash
./bin/sendy router --addr :9090       # Listen address
./bin/sendy router --logdir logs      # Log directory
```

### Chat Client

```bash
./bin/sendy --router localhost:9090   # Router address
./bin/sendy --data ~/.sendy            # Data directory
./bin/sendy --genkey                   # Generate keys only
```

### Available Commands

```bash
sendy              # Start chat client (default)
sendy chat         # Start chat client
sendy router       # Start router server
sendy --help       # Show help
sendy chat --help  # Show chat options
sendy router --help # Show router options
```

### Environment Variables

- `DEBUG=1` - Enable debug logging

### Limits

```go
MaxFileSize     = 200 MB     // Maximum file transfer size
MaxMessageSize  = 10 MB      // Maximum message size
MaxContactName  = 256 bytes  // Maximum contact name length
MaxContactCount = 10000      // Maximum contacts per user
```

## Project Structure

```
sendy/
├── cmd/
│   └── sendy/            # Unified CLI application
│       ├── main.go       # Entry point
│       └── cmd/          # Cobra commands
│           ├── root.go   # Root command
│           ├── chat.go   # Chat client command
│           └── router.go # Router server command
├── router/               # Router server and client
│   ├── router.go         # Server implementation
│   ├── client.go         # Client library
│   ├── types.go          # Protocol types
│   └── const.go          # Constants
├── p2p/                  # WebRTC P2P connector
│   ├── webrtc.go         # Connection management
│   ├── crypto.go         # End-to-end encryption
│   └── *_test.go         # Tests
├── chat/                 # Chat logic
│   ├── chat.go           # Core chat logic
│   ├── storage.go        # SQLite persistence
│   ├── tui.go            # Bubbletea TUI
│   └── filepicker_external.go  # fzf integration
├── SECURITY.md           # Security documentation
├── LICENSE               # MIT License
└── README.md             # This file
```

## Development

### Building

```bash
# Build unified CLI
go build -o bin/sendy ./cmd/sendy

# Build with debug symbols stripped (smaller binary)
go build -ldflags="-s -w" -o bin/sendy ./cmd/sendy

# Or use Makefile
make build           # Normal build
make build-release   # Optimized build
make clean           # Clean build artifacts
```

### Testing

```bash
# Run all tests
go test ./...

# Test specific package
go test ./router
go test ./p2p
go test ./chat

# Run with verbose output
go test -v ./...

# Run benchmarks
go test -bench=. -benchtime=5s ./router
go test -bench=. -benchtime=5s ./p2p
```

### Running Tests with Real Peers

```bash
# Terminal 1: Start router
./sendy router

# Terminal 2: Start Alice
./sendy chat --data /tmp/alice

# Terminal 3: Start Bob
./sendy chat --data /tmp/bob

# Add each other as contacts and test messaging/file transfer
```

## Roadmap

**Security Improvements:**
- [ ] Perfect Forward Secrecy (PFS) implementation
- [ ] Key rotation support
- [ ] Optional key encryption with passphrase
- [ ] Multi-device key synchronization

**Features:**
- [ ] Group chats
- [ ] Voice/video calls (WebRTC media streams)
- [ ] Message reactions and replies
- [ ] Message search
- [ ] Contact verification (QR codes)
- [ ] Desktop notifications

**UX Improvements:**
- [ ] File transfer progress bars
- [ ] Image preview in terminal
- [ ] Emoji support
- [ ] Customizable themes
- [ ] Message timestamps in chat view

**Operations:**
- [ ] Configurable STUN/TURN servers
- [ ] Docker images
- [ ] systemd service files
- [ ] Configuration file support

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

Before contributing:
1. Read [SECURITY.md](SECURITY.md) to understand the security model
2. Ensure all tests pass: `go test ./...`
3. Add tests for new features
4. Follow Go best practices and conventions
5. Update documentation as needed

## Dependencies

- [spf13/cobra](https://github.com/spf13/cobra) - CLI framework
- [pion/webrtc](https://github.com/pion/webrtc) - WebRTC implementation
- [charmbracelet/bubbletea](https://github.com/charmbracelet/bubbletea) - TUI framework
- [mattn/go-sqlite3](https://github.com/mattn/go-sqlite3) - SQLite driver
- [golang.org/x/crypto](https://golang.org/x/crypto) - Cryptographic primitives

## License

MIT License - see [LICENSE](LICENSE) file for details.

## Acknowledgments

- Built with [Pion WebRTC](https://github.com/pion/webrtc)
- UI powered by [Charm Bracelet's Bubbletea](https://github.com/charmbracelet/bubbletea)
- Cryptography from [NaCl/box](https://nacl.cr.yp.to/box.html) and Go's crypto libraries

---

**⚠️ Security Notice:** Sendy is experimental software. While it implements strong cryptography, it has not undergone a professional security audit. Use at your own risk. See [SECURITY.md](SECURITY.md) for details.
