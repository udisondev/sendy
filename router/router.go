package router

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"sync"
	"time"
)

func Run(addr string) error {
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("net.Listen: %w", err)
	}

	var peers sync.Map
	authPool := sync.Pool{
		New: func() any {
			return make([]byte, ed25519.PublicKeySize+ChallangeSize+ed25519.SignatureSize)
		},
	}
	hp := sync.Pool{
		New: func() any {
			return make([]byte, MaxPacketSize)
		},
	}
	slog.Info("Router listening", "address", addr)
	for {
		conn, err := lis.Accept()
		if err != nil {
			slog.Error("Failed to accept connection", "error", err)
			return fmt.Errorf("lis.Accept: %w", err)
		}

		slog.Debug("Accepted new connection", "remoteAddr", conn.RemoteAddr().String())
		go handleConn(conn, &peers, &authPool, &hp)
	}
}

func handleConn(conn net.Conn, peers *sync.Map, authPool *sync.Pool, hp *sync.Pool) {
	remoteAddr := conn.RemoteAddr().String()
	defer conn.Close()

	slog.Debug("Starting authentication", "remoteAddr", remoteAddr)
	id, err := auth(conn, AuthTimeout, authPool)
	if err != nil {
		slog.Error("Failed to authenticate new connection", "remoteAddr", remoteAddr, "error", err)
		return
	}

	hexID := hex.EncodeToString(id[:])
	slog.Info("Peer authenticated", "hexID", hexID, "remoteAddr", remoteAddr)

	peer := &Peer{
		ID:           id,
		conn:         conn,
		writeTimeout: WriteTimeout,
	}
	peers.Store(id, peer)
	slog.Debug("Peer stored in map", "hexID", hexID)

	defer func() {
		peers.Delete(id)
		slog.Debug("Peer removed from map", "hexID", hexID)
	}()

	for {
		if err := handleMessage(peer, peers, hp); err != nil {
			// EOF or closed connection is normal - peer disconnected gracefully
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				slog.Info("Peer disconnected gracefully", "hexID", hexID)
			} else {
				slog.Error("Failed to read message from peer", "hexID", hexID, "error", err)
			}
			return
		}
	}
}

func handleMessage(peer *Peer, peers *sync.Map, hp *sync.Pool) error {
	buf := hp.Get().([]byte)
	defer hp.Put(buf)

	// Read header: MessageLen(4) + RequestID(12) + Recipient(32) = 48 bytes
	if _, err := io.ReadFull(peer.conn, buf[:PeerHeaderSize]); err != nil {
		return fmt.Errorf("read header: %w", err)
	}

	// Parse message length
	mlen := binary.BigEndian.Uint32(buf[:4])
	if mlen > MaxPacketSize {
		slog.Warn("Message too big", "from", hex.EncodeToString(peer.ID[:8]), "size", mlen, "max", MaxPacketSize)
		return fmt.Errorf("message input is too big: %d bytes", mlen)
	}

	// Parse RequestID and Recipient from buffer
	// Store reqID at end of buffer to avoid overlap during copy
	of := 4
	reqIDOffset := MaxPacketSize - RequestIDSize
	copy(buf[reqIDOffset:reqIDOffset+RequestIDSize], buf[of:of+RequestIDSize])
	reqID := buf[reqIDOffset : reqIDOffset+RequestIDSize]
	of += RequestIDSize
	var recipient PeerID
	copy(recipient[:], buf[of:of+PeerIDSize])
	of += PeerIDSize

	// Calculate payload length
	payloadLen := mlen - RequestIDSize - PeerIDSize

	slog.Debug("Routing message",
		"from", hex.EncodeToString(peer.ID[:8]),
		"to", hex.EncodeToString(recipient[:8]),
		"payloadLen", payloadLen,
		"reqID", hex.EncodeToString(reqID[:4]))

	// Find recipient peer
	recipientVal, ok := peers.Load(recipient)
	if !ok {
		slog.Debug("Recipient not found, sending NotFound",
			"recipient", hex.EncodeToString(recipient[:8]),
			"from", hex.EncodeToString(peer.ID[:8]))
		// Recipient not found - skip payload and send NotFound
		if payloadLen > 0 {
			// Use part of buffer for CopyBuffer (avoid allocation in io.Copy)
			discardBuf := buf[PeerHeaderSize : PeerHeaderSize+8192]
			if _, err := io.CopyBuffer(io.Discard, io.LimitReader(peer.conn, int64(payloadLen)), discardBuf); err != nil {
				return fmt.Errorf("discard payload: %w", err)
			}
		}
		// Reuse buf for NotFound: MessageLen(4) + Type(1) + RequestID(12) = 17 bytes
		binary.BigEndian.PutUint32(buf[0:4], 1+RequestIDSize)
		buf[4] = byte(NotFound)
		copy(buf[5:5+RequestIDSize], reqID)
		_, err := peer.conn.Write(buf[:5+RequestIDSize])
		return err
	}

	recipientPeer := recipientVal.(*Peer)

	// Reuse buf for Income: MessageLen(4) + Type(1) + RequestID(12) + SenderID(32)
	incomeHeaderLen := 4 + 1 + RequestIDSize + PeerIDSize
	binary.BigEndian.PutUint32(buf[0:4], uint32(1+RequestIDSize+PeerIDSize+payloadLen))
	buf[4] = byte(Income)
	copy(buf[5:5+RequestIDSize], reqID)
	copy(buf[5+RequestIDSize:5+RequestIDSize+PeerIDSize], peer.ID[:])

	// Send Income to recipient
	recipientPeer.mu.Lock()
	recipientPeer.conn.SetWriteDeadline(time.Now().Add(recipientPeer.writeTimeout))

	// Write Income header
	if _, err := recipientPeer.conn.Write(buf[:incomeHeaderLen]); err != nil {
		recipientPeer.conn.SetWriteDeadline(time.Time{})
		recipientPeer.mu.Unlock()

		// Send error - send Error to sender
		binary.BigEndian.PutUint32(buf[0:4], 1+RequestIDSize)
		buf[4] = byte(Error)
		copy(buf[5:5+RequestIDSize], reqID)
		peer.conn.Write(buf[:5+RequestIDSize])
		return fmt.Errorf("send to recipient: %w", err)
	}

	// Zero-copy: copy payload directly from sender conn to recipient conn
	if payloadLen > 0 {
		// Use part of buffer for CopyBuffer (avoid allocation in io.Copy)
		copyBuf := buf[incomeHeaderLen : incomeHeaderLen+8192]
		_, err := io.CopyBuffer(recipientPeer.conn, io.LimitReader(peer.conn, int64(payloadLen)), copyBuf)
		recipientPeer.conn.SetWriteDeadline(time.Time{})
		recipientPeer.mu.Unlock()

		if err != nil {
			slog.Error("Failed to copy payload to recipient",
				"from", hex.EncodeToString(peer.ID[:8]),
				"to", hex.EncodeToString(recipient[:8]),
				"payloadLen", payloadLen,
				"error", err)

			// Send error - send Error to sender
			binary.BigEndian.PutUint32(buf[0:4], 1+RequestIDSize)
			buf[4] = byte(Error)
			copy(buf[5:5+RequestIDSize], reqID)
			peer.conn.Write(buf[:5+RequestIDSize])
			return fmt.Errorf("copy payload: %w", err)
		}
	} else {
		recipientPeer.conn.SetWriteDeadline(time.Time{})
		recipientPeer.mu.Unlock()
	}

	slog.Debug("Message delivered successfully",
		"from", hex.EncodeToString(peer.ID[:8]),
		"to", hex.EncodeToString(recipient[:8]),
		"payloadLen", payloadLen)

	// Send Success to sender (reuse buf)
	binary.BigEndian.PutUint32(buf[0:4], 1+RequestIDSize)
	buf[4] = byte(Success)
	copy(buf[5:5+RequestIDSize], reqID)
	_, err := peer.conn.Write(buf[:5+RequestIDSize])
	return err
}

var ErrAuthFailed = errors.New("authentication failed")

func auth(conn net.Conn, timeout time.Duration, authPool *sync.Pool) (PeerID, error) {
	id := PeerID{}
	conn.SetDeadline(time.Now().Add(timeout))
	defer conn.SetDeadline(time.Time{})

	buf := authPool.Get().([]byte)
	defer authPool.Put(buf)

	of := 0
	pubkey := buf[of:ed25519.PublicKeySize]
	of += ed25519.PublicKeySize
	challange := buf[of : of+ChallangeSize]
	of += ChallangeSize
	sig := buf[of : of+ed25519.SignatureSize]

	if _, err := io.ReadFull(conn, pubkey); err != nil {
		return id, fmt.Errorf("read public key: %w", err)
	}

	if _, err := rand.Read(challange); err != nil {
		return id, fmt.Errorf("generate challange: %w", err)
	}

	if _, err := conn.Write(challange); err != nil {
		return id, fmt.Errorf("send challange: %w", err)
	}

	if _, err := io.ReadFull(conn, sig); err != nil {
		return id, fmt.Errorf("read signature: %w", err)
	}

	if !ed25519.Verify(pubkey, challange, sig) {
		return id, ErrAuthFailed
	}

	copy(id[:], pubkey)

	return id, nil
}
