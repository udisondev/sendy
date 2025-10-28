package router

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"io"
	mrand "math/rand"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRouterThroughput(t *testing.T) {
	// Запускаем роутер
	addr := "127.0.0.1:0"
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()

	addr = lis.Addr().String()

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

	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			go handleConn(conn, &peers, &authPool, &hp)
		}
	}()

	// Создаем два клиента
	client1, privKey1 := createAuthenticatedClient(t, addr)
	defer client1.Close()

	client2, _ := createAuthenticatedClient(t, addr)
	defer client2.Close()

	// Даем время на регистрацию
	time.Sleep(100 * time.Millisecond)

	// Отправляем сообщения от client1 к client2
	pubKey1 := privKey1.Public().(ed25519.PublicKey)
	var recipient PeerID
	copy(recipient[:], pubKey1)

	payload := make([]byte, 1024) // 1KB payload
	rand.Read(payload)

	start := time.Now()
	messageCount := 1000

	var wg sync.WaitGroup
	wg.Add(1)

	// Читаем ответы в отдельной горутине
	go func() {
		defer wg.Done()
		for i := 0; i < messageCount; i++ {
			// Читаем ServerMessage (Success)
			if _, err := readServerMessage(client1); err != nil {
				t.Errorf("failed to read response: %v", err)
				return
			}
		}
	}()

	// Отправляем сообщения
	for i := 0; i < messageCount; i++ {
		var reqID RequestID
		rand.Read(reqID[:])

		msg := PeerMessage{
			RequestID: reqID,
			Recipient: recipient,
			Payload:   payload,
		}

		if err := writePeerMessage(client1, msg); err != nil {
			t.Fatalf("failed to send message: %v", err)
		}
	}

	wg.Wait()
	elapsed := time.Since(start)

	throughput := float64(messageCount) / elapsed.Seconds()
	dataThroughput := float64(messageCount*len(payload)) / elapsed.Seconds() / 1024 / 1024 // MB/s

	t.Logf("Sent %d messages in %v", messageCount, elapsed)
	t.Logf("Throughput: %.2f msg/s", throughput)
	t.Logf("Data throughput: %.2f MB/s", dataThroughput)
	t.Logf("Average latency: %v", elapsed/time.Duration(messageCount))
}

func TestRouter10KPeers(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping 10K peers test in short mode")
	}

	// Запускаем роутер
	addr := "127.0.0.1:0"
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()

	addr = lis.Addr().String()

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

	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			go handleConn(conn, &peers, &authPool, &hp)
		}
	}()

	// Создаем 10,000 клиентов
	peerCount := 10000
	t.Logf("Connecting %d peers...", peerCount)

	type peerInfo struct {
		conn   net.Conn
		id     PeerID
		privKey ed25519.PrivateKey
	}

	peerList := make([]*peerInfo, peerCount)

	start := time.Now()
	for i := 0; i < peerCount; i++ {
		conn, privKey := createAuthenticatedClient(t, addr)
		pubKey := privKey.Public().(ed25519.PublicKey)
		var id PeerID
		copy(id[:], pubKey)

		peerList[i] = &peerInfo{
			conn:   conn,
			id:     id,
			privKey: privKey,
		}

		if (i+1)%1000 == 0 {
			t.Logf("Connected %d/%d peers", i+1, peerCount)
		}
	}
	elapsed := time.Since(start)
	t.Logf("All %d peers connected in %v (%.0f peers/sec)", peerCount, elapsed, float64(peerCount)/elapsed.Seconds())

	// Даем время на регистрацию
	time.Sleep(500 * time.Millisecond)

	// Каждый пир отправляет сообщение 3 случайным пирам
	messagesPerPeer := 3
	totalMessages := peerCount * messagesPerPeer

	t.Logf("Sending %d messages (%d peers × %d messages)...", totalMessages, peerCount, messagesPerPeer)

	var sendWg sync.WaitGroup
	var recvWg sync.WaitGroup

	// Счетчик полученных сообщений
	var receivedCount int64

	// Запускаем горутины для чтения ответов
	for _, p := range peerList {
		recvWg.Add(1)
		go func(peer *peerInfo) {
			defer recvWg.Done()
			for j := 0; j < messagesPerPeer; j++ {
				if _, err := readServerMessage(peer.conn); err != nil {
					return
				}
				atomic.AddInt64(&receivedCount, 1)
			}
		}(p)
	}

	start = time.Now()

	// Отправляем сообщения
	for i, sender := range peerList {
		sendWg.Add(1)
		go func(idx int, peer *peerInfo) {
			defer sendWg.Done()

			payload := make([]byte, 256)
			rand.Read(payload)

			for j := 0; j < messagesPerPeer; j++ {
				// Выбираем случайного получателя (не себя)
				recipientIdx := mrand.Intn(peerCount)
				for recipientIdx == idx {
					recipientIdx = mrand.Intn(peerCount)
				}

				var reqID RequestID
				rand.Read(reqID[:])

				msg := PeerMessage{
					RequestID: reqID,
					Recipient: peerList[recipientIdx].id,
					Payload:   payload,
				}

				if err := writePeerMessage(peer.conn, msg); err != nil {
					t.Errorf("Failed to send message: %v", err)
					return
				}
			}
		}(i, sender)

		if (i+1)%1000 == 0 {
			time.Sleep(10 * time.Millisecond) // Небольшая задержка чтобы не перегрузить систему
		}
	}

	sendWg.Wait()
	recvWg.Wait()

	elapsed = time.Since(start)

	t.Logf("Exchanged %d messages in %v", totalMessages, elapsed)
	t.Logf("Throughput: %.0f msg/s", float64(totalMessages)/elapsed.Seconds())
	t.Logf("Data throughput: %.2f MB/s", float64(totalMessages*256)/elapsed.Seconds()/1024/1024)
	t.Logf("Average latency: %v", elapsed/time.Duration(totalMessages))
	t.Logf("Received responses: %d/%d", receivedCount, totalMessages)

	// Закрываем все соединения
	for _, p := range peerList {
		p.conn.Close()
	}
}

func BenchmarkRouterZeroCopy(b *testing.B) {
	hp := sync.Pool{
		New: func() any {
			return make([]byte, MaxPacketSize)
		},
	}

	b.ResetTimer()
	b.ReportAllocs()

	for i := 0; i < b.N; i++ {
		buf := hp.Get().([]byte)

		// Симулируем обработку сообщения
		// Парсинг заголовка
		mlen := uint32(1024 + RequestIDSize + PeerIDSize)
		of := 4
		reqID := buf[of : of+RequestIDSize]
		of += RequestIDSize
		var recipient PeerID
		copy(recipient[:], buf[of:of+PeerIDSize])

		// Формирование Income ответа
		incomeHeaderLen := 4 + 1 + RequestIDSize + PeerIDSize
		binary.BigEndian.PutUint32(buf[0:4], uint32(1+RequestIDSize+PeerIDSize+1024))
		buf[4] = byte(Income)
		copy(buf[5:5+RequestIDSize], reqID)
		copy(buf[5+RequestIDSize:5+RequestIDSize+PeerIDSize], recipient[:])

		// Формирование Success ответа
		binary.BigEndian.PutUint32(buf[0:4], 1+RequestIDSize)
		buf[4] = byte(Success)
		copy(buf[5:5+RequestIDSize], reqID)

		_ = mlen
		_ = incomeHeaderLen

		hp.Put(buf)
	}
}

// Вспомогательные функции

func createAuthenticatedClient(tb testing.TB, addr string) (net.Conn, ed25519.PrivateKey) {
	tb.Helper()

	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		tb.Fatal(err)
	}

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		tb.Fatal(err)
	}

	// Отправляем публичный ключ
	if _, err := conn.Write(pubKey); err != nil {
		tb.Fatal(err)
	}

	// Читаем challenge
	challenge := make([]byte, ChallangeSize)
	if _, err := io.ReadFull(conn, challenge); err != nil {
		tb.Fatal(err)
	}

	// Подписываем и отправляем
	signature := ed25519.Sign(privKey, challenge)
	if _, err := conn.Write(signature); err != nil {
		tb.Fatal(err)
	}

	return conn, privKey
}

func writePeerMessage(conn net.Conn, msg PeerMessage) error {
	// Вычисляем длину сообщения: RequestID(12) + Recipient(32) + Payload
	messageLen := uint32(12 + 32 + len(msg.Payload))

	// Message length
	lenBuf := make([]byte, 4)
	lenBuf[0] = byte(messageLen >> 24)
	lenBuf[1] = byte(messageLen >> 16)
	lenBuf[2] = byte(messageLen >> 8)
	lenBuf[3] = byte(messageLen)
	if _, err := conn.Write(lenBuf); err != nil {
		return err
	}

	// RequestID
	if _, err := conn.Write(msg.RequestID[:]); err != nil {
		return err
	}

	// Recipient
	if _, err := conn.Write(msg.Recipient[:]); err != nil {
		return err
	}

	// Payload
	if len(msg.Payload) > 0 {
		if _, err := conn.Write(msg.Payload); err != nil {
			return err
		}
	}

	return nil
}

func readServerMessage(conn net.Conn) (ServerMessage, error) {
	var msg ServerMessage

	// Message length
	lenBuf := make([]byte, 4)
	if _, err := io.ReadFull(conn, lenBuf); err != nil {
		return msg, err
	}
	messageLen := uint32(lenBuf[0])<<24 | uint32(lenBuf[1])<<16 | uint32(lenBuf[2])<<8 | uint32(lenBuf[3])

	// Type
	typeBuf := make([]byte, 1)
	if _, err := io.ReadFull(conn, typeBuf); err != nil {
		return msg, err
	}
	msg.Type = SMType(typeBuf[0])

	// RequestID
	if _, err := io.ReadFull(conn, msg.RequestID[:]); err != nil {
		return msg, err
	}

	// Для Income читаем SenderID и Payload
	if msg.Type == Income {
		if _, err := io.ReadFull(conn, msg.SenderID[:]); err != nil {
			return msg, err
		}

		// Вычисляем длину payload: messageLen - Type(1) - RequestID(12) - SenderID(32)
		payloadLen := messageLen - 1 - 12 - 32

		if payloadLen > 0 {
			msg.Payload = make([]byte, payloadLen)
			if _, err := io.ReadFull(conn, msg.Payload); err != nil {
				return msg, err
			}
		}
	}

	return msg, nil
}

func TestClientBasic(t *testing.T) {
	// Запускаем роутер
	addr := "127.0.0.1:0"
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()

	addr = lis.Addr().String()

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

	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			go handleConn(conn, &peers, &authPool, &hp)
		}
	}()

	// Создаем два клиента
	pubKey1, privKey1, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	pubKey2, privKey2, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	client1 := NewClient(pubKey1, privKey1)
	client2 := NewClient(pubKey2, privKey2)

	ctx := context.Background()
	income1, err := client1.Dial(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}

	income2, err := client2.Dial(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}

	// Читаем Income сообщения
	go func() {
		for msg := range income1 {
			t.Logf("Client1 received Income: type=%v", msg.Type)
		}
	}()

	go func() {
		for msg := range income2 {
			t.Logf("Client2 received Income: type=%v", msg.Type)
		}
	}()

	// Даем время на регистрацию
	time.Sleep(100 * time.Millisecond)

	// Client1 отправляет сообщение Client2
	var recipient PeerID
	copy(recipient[:], pubKey2)

	respCh, err := client1.Send(ctx, recipient, []byte("hello"))
	if err != nil {
		t.Fatal(err)
	}

	select {
	case msg, ok := <-respCh:
		if !ok {
			t.Fatal("Channel closed without response")
		}
		if msg.Type != Success {
			t.Fatalf("Expected Success, got %v", msg.Type)
		}
		t.Logf("Client1 received response: type=%v", msg.Type)
	case <-time.After(time.Second):
		t.Fatal("Timeout waiting for response")
	}
}

func TestClientTimeout(t *testing.T) {
	// Запускаем роутер
	addr := "127.0.0.1:0"
	lis, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer lis.Close()

	addr = lis.Addr().String()

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

	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			go handleConn(conn, &peers, &authPool, &hp)
		}
	}()

	// Создаем клиент
	pubKey, privKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	client := NewClient(pubKey, privKey)
	client.SetRequestTimeout(500 * time.Millisecond) // Увеличим timeout

	ctx := context.Background()
	income, err := client.Dial(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}

	// Читаем Income сообщения в отдельной горутине
	go func() {
		for msg := range income {
			t.Logf("Received Income: %+v", msg)
		}
	}()

	// Даем время на регистрацию
	time.Sleep(100 * time.Millisecond)

	// Отправляем сообщение несуществующему пиру
	var recipient PeerID
	rand.Read(recipient[:])

	t.Logf("Sending to recipient: %x", recipient[:])

	start := time.Now()
	respCh, err := client.Send(ctx, recipient, []byte("test"))
	if err != nil {
		t.Fatal(err)
	}

	// Ждем ответа (должен быть NotFound, не timeout)
	select {
	case msg, ok := <-respCh:
		elapsed := time.Since(start)
		if !ok {
			t.Fatalf("Channel closed without response (timeout after %v)", elapsed)
		}
		if msg.Type != NotFound {
			t.Fatalf("Expected NotFound, got %v", msg.Type)
		}
		t.Logf("Response received in %v (type: %v)", elapsed, msg.Type)
	case <-time.After(200 * time.Millisecond):
		t.Fatal("Test timeout - no response received")
	}
}

