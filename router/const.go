package router

import (
	"crypto/ed25519"
	"time"
)

const (
	ChallangeSize  = 32
	PeerIDSize     = ed25519.PublicKeySize
	AuthTimeout    = 5 * time.Second // SECURITY: Увеличен с 1s до 5s для медленных соединений
	WriteTimeout   = 5 * time.Second // SECURITY: Увеличен для консистентности
	RequestIDSize  = 12
	MaxPacketSize  = 32 * 1024 // 32 KB
	PeerHeaderSize = 4 + RequestIDSize + PeerIDSize
)
