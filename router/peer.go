package router

import (
	"net"
	"sync"
	"time"
)

type PeerID [PeerIDSize]byte

type Peer struct {
	ID           PeerID
	conn         net.Conn
	writeTimeout time.Duration
	mu           sync.Mutex
}
