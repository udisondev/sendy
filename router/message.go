package router

type RequestID [RequestIDSize]byte

type PeerMessage struct {
	RequestID RequestID
	Recipient PeerID
	Payload   []byte
}

type ServerMessage struct {
	Type      SMType
	RequestID RequestID
	SenderID  PeerID
	Payload   []byte
}

type SMType uint8

const (
	Success SMType = iota
	Error
	NotFound
	Income
)
