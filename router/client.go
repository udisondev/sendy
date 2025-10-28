package router

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

type Client struct {
	pubkey     ed25519.PublicKey
	privkey    ed25519.PrivateKey
	conn       net.Conn
	mu         sync.Mutex
	reqMap     map[RequestID]chan ServerMessage
	writeBuf   [PeerHeaderSize]byte
	reqTimeout time.Duration
}

func NewClient(pubkey ed25519.PublicKey, privkey ed25519.PrivateKey) *Client {
	return &Client{
		pubkey:     pubkey,
		privkey:    privkey,
		reqMap:     make(map[RequestID]chan ServerMessage),
		reqTimeout: 5 * time.Second,
	}
}

func (c *Client) SetRequestTimeout(timeout time.Duration) {
	c.mu.Lock()
	c.reqTimeout = timeout
	c.mu.Unlock()
}

func (c *Client) GetPublicKey() ed25519.PublicKey {
	return c.pubkey
}

func (c *Client) Dial(ctx context.Context, addr string) (<-chan ServerMessage, error) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("net.Dial: %w", err)
	}

	c.conn = conn

	income := make(chan ServerMessage, 100)
	go func() {
		<-ctx.Done()
		close(income)
		conn.Close()
	}()

	if err := c.signUp(conn); err != nil {
		return nil, err
	}

	go func() {
		defer conn.Close()
		for {
			msg, err := c.readServerMessage()
			if err != nil {
				return
			}

			if msg.Type == Income {
				select {
				case income <- msg:
				case <-ctx.Done():
					return
				}
			} else {
				c.mu.Lock()
				ch, ok := c.reqMap[msg.RequestID]
				if ok {
					delete(c.reqMap, msg.RequestID)
				}
				c.mu.Unlock()
				if !ok {
					continue
				}

				select {
				case ch <- msg:
				case <-ctx.Done():
					return
				}
			}
		}
	}()

	return income, nil
}

func (c *Client) signUp(conn net.Conn) error {
	if _, err := conn.Write(c.pubkey); err != nil {
		return fmt.Errorf("send public key: %w", err)
	}

	challange := make([]byte, ChallangeSize)
	if _, err := io.ReadFull(conn, challange); err != nil {
		return fmt.Errorf("read challange: %w", err)
	}

	sig := ed25519.Sign(c.privkey, challange)
	if _, err := conn.Write(sig); err != nil {
		return fmt.Errorf("send signature: %w", err)
	}

	return nil
}

func (c *Client) readServerMessage() (ServerMessage, error) {
	var msg ServerMessage
	var headerBuf [5]byte // MessageLen(4) + Type(1)

	// Читаем MessageLen и Type
	if _, err := io.ReadFull(c.conn, headerBuf[:]); err != nil {
		return msg, err
	}

	messageLen := binary.BigEndian.Uint32(headerBuf[0:4])
	msg.Type = SMType(headerBuf[4])

	// RequestID (12 bytes)
	if _, err := io.ReadFull(c.conn, msg.RequestID[:]); err != nil {
		return msg, err
	}

	// Для Income читаем SenderID и Payload
	if msg.Type == Income {
		if _, err := io.ReadFull(c.conn, msg.SenderID[:]); err != nil {
			return msg, err
		}

		// Вычисляем длину payload: messageLen - Type(1) - RequestID(12) - SenderID(32)
		payloadLen := messageLen - 1 - RequestIDSize - PeerIDSize

		if payloadLen > 0 {
			msg.Payload = make([]byte, payloadLen)
			if _, err := io.ReadFull(c.conn, msg.Payload); err != nil {
				return msg, err
			}
		}
	}

	return msg, nil
}

func (c *Client) Send(ctx context.Context, recipient PeerID, payload []byte) (<-chan ServerMessage, error) {
	var reqID RequestID
	if _, err := rand.Read(reqID[:]); err != nil {
		return nil, fmt.Errorf("generate request id: %w", err)
	}

	respCh := make(chan ServerMessage, 1)

	// Захватываем timeout до создания горутины
	c.mu.Lock()
	timeout := c.reqTimeout
	c.mu.Unlock()

	// Добавляем в мапу ДО отправки сообщения
	c.mu.Lock()
	c.reqMap[reqID] = respCh
	c.mu.Unlock()

	go func() {
		<-time.After(timeout)
		c.mu.Lock()
		defer c.mu.Unlock()

		ch, ok := c.reqMap[reqID]
		if !ok {
			return
		}

		delete(c.reqMap, reqID)
		close(ch)
	}()

	msg := PeerMessage{
		RequestID: reqID,
		Recipient: recipient,
		Payload:   payload,
	}

	if err := c.writePeerMessage(msg); err != nil {
		c.mu.Lock()
		delete(c.reqMap, reqID)
		c.mu.Unlock()
		return nil, err
	}

	return respCh, nil
}

func (c *Client) writePeerMessage(msg PeerMessage) error {
	// Вычисляем длину сообщения: RequestID(12) + Recipient(32) + Payload
	messageLen := uint32(RequestIDSize + PeerIDSize + len(msg.Payload))

	c.mu.Lock()
	defer c.mu.Unlock()

	// Формируем заголовок: MessageLen(4) + RequestID(12) + Recipient(32)
	binary.BigEndian.PutUint32(c.writeBuf[0:4], messageLen)
	copy(c.writeBuf[4:4+RequestIDSize], msg.RequestID[:])
	copy(c.writeBuf[4+RequestIDSize:4+RequestIDSize+PeerIDSize], msg.Recipient[:])

	// Отправляем заголовок
	if _, err := c.conn.Write(c.writeBuf[:]); err != nil {
		return err
	}

	// Payload
	if len(msg.Payload) > 0 {
		if _, err := c.conn.Write(msg.Payload); err != nil {
			return err
		}
	}

	return nil
}
