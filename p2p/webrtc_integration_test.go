package p2p

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"sendy/router"
)

func TestWebRTCIntegration(t *testing.T) {
	// Запускаем router сервер
	addr := "localhost:18080"
	go func() {
		if err := router.Run(addr); err != nil {
			t.Logf("Router server error: %v", err)
		}
	}()

	// Даем серверу время запуститься
	time.Sleep(100 * time.Millisecond)

	// Создаем два пира
	pubkey1, privkey1, _ := ed25519.GenerateKey(nil)
	pubkey2, privkey2, _ := ed25519.GenerateKey(nil)

	peerID1 := router.PeerID{}
	peerID2 := router.PeerID{}
	copy(peerID1[:], pubkey1)
	copy(peerID2[:], pubkey2)

	t.Logf("Peer1 ID: %s", hex.EncodeToString(peerID1[:]))
	t.Logf("Peer2 ID: %s", hex.EncodeToString(peerID2[:]))

	// Подключаем первого пира к router
	client1 := router.NewClient(pubkey1, privkey1)
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()

	income1, err := client1.Dial(ctx1, addr)
	if err != nil {
		t.Fatalf("Peer1 dial failed: %v", err)
	}
	t.Log("Peer1 connected to router")

	// Подключаем второго пира к router
	client2 := router.NewClient(pubkey2, privkey2)
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()

	income2, err := client2.Dial(ctx2, addr)
	if err != nil {
		t.Fatalf("Peer2 dial failed: %v", err)
	}
	t.Log("Peer2 connected to router")

	// Создаем WebRTC коннекторы
	cfg := ConnectorConfig{
		STUNServers: []string{"stun:stun.l.google.com:19302"},
	}

	connector1, err := NewConnector(client1, cfg, income1, privkey1)
	if err != nil {
		t.Fatalf("Failed to create connector1: %v", err)
	}
	connector2, err := NewConnector(client2, cfg, income2, privkey2)
	if err != nil {
		t.Fatalf("Failed to create connector2: %v", err)
	}

	// Каналы для синхронизации
	peer1Connected := make(chan struct{})
	peer2Connected := make(chan struct{})
	peer1ReceivedData := make(chan string, 1)
	peer2ReceivedData := make(chan string, 1)

	// Обработчик событий для peer1
	go func() {
		for event := range connector1.Events() {
			switch event.Type {
			case EventConnected:
				t.Logf("Peer1: Connected to %s", hex.EncodeToString(event.PeerID[:]))
				close(peer1Connected)

			case EventDisconnected:
				t.Logf("Peer1: Disconnected from %s", hex.EncodeToString(event.PeerID[:]))

			case EventConnectionFailed:
				t.Logf("Peer1: Connection failed: %v", event.Error)

			case EventDataReceived:
				msg := string(event.Data)
				t.Logf("Peer1: Received data: %s", msg)
				peer1ReceivedData <- msg

			case EventError:
				t.Logf("Peer1: Error: %v", event.Error)
			}
		}
	}()

	// Обработчик событий для peer2
	go func() {
		for event := range connector2.Events() {
			switch event.Type {
			case EventConnected:
				t.Logf("Peer2: Connected to %s", hex.EncodeToString(event.PeerID[:]))
				close(peer2Connected)

			case EventDisconnected:
				t.Logf("Peer2: Disconnected from %s", hex.EncodeToString(event.PeerID[:]))

			case EventConnectionFailed:
				t.Logf("Peer2: Connection failed: %v", event.Error)

			case EventDataReceived:
				msg := string(event.Data)
				t.Logf("Peer2: Received data: %s", msg)
				peer2ReceivedData <- msg

			case EventError:
				t.Logf("Peer2: Error: %v", event.Error)
			}
		}
	}()

	// Peer1 инициирует подключение к Peer2
	t.Log("Peer1: Initiating connection to Peer2...")
	hexID2 := hex.EncodeToString(peerID2[:])
	if err := connector1.Connect(hexID2); err != nil {
		t.Fatalf("Connect failed: %v", err)
	}

	// Ждем установки соединения с обеих сторон
	t.Log("Waiting for WebRTC connection to establish...")
	timeout := time.After(30 * time.Second)

	select {
	case <-peer1Connected:
		t.Log("Peer1: WebRTC connection established")
	case <-timeout:
		t.Fatal("Timeout waiting for peer1 connection")
	}

	select {
	case <-peer2Connected:
		t.Log("Peer2: WebRTC connection established")
	case <-timeout:
		t.Fatal("Timeout waiting for peer2 connection")
	}

	// Даем DataChannel время полностью открыться
	time.Sleep(500 * time.Millisecond)

	// Отправляем сообщение от Peer1 к Peer2
	peer, ok := connector1.GetPeer(peerID2)
	if !ok {
		t.Fatal("Peer2 not found in connector1")
	}

	t.Log("Peer1: Sending message to Peer2...")
	msg1 := "Hello from Peer1!"
	if err := peer.Send([]byte(msg1)); err != nil {
		t.Fatalf("Peer1 send failed: %v", err)
	}

	// Отправляем сообщение от Peer2 к Peer1
	peer, ok = connector2.GetPeer(peerID1)
	if !ok {
		t.Fatal("Peer1 not found in connector2")
	}

	t.Log("Peer2: Sending message to Peer1...")
	msg2 := "Hello from Peer2!"
	if err := peer.Send([]byte(msg2)); err != nil {
		t.Fatalf("Peer2 send failed: %v", err)
	}

	// Проверяем что оба пира получили сообщения
	t.Log("Waiting for messages...")

	select {
	case received := <-peer2ReceivedData:
		if received != msg1 {
			t.Fatalf("Peer2 received wrong message: got %q, want %q", received, msg1)
		}
		t.Log("✓ Peer2 received correct message from Peer1")
	case <-time.After(10 * time.Second):
		t.Fatal("Timeout waiting for peer2 to receive data")
	}

	select {
	case received := <-peer1ReceivedData:
		if received != msg2 {
			t.Fatalf("Peer1 received wrong message: got %q, want %q", received, msg2)
		}
		t.Log("✓ Peer1 received correct message from Peer2")
	case <-time.After(10 * time.Second):
		t.Fatal("Timeout waiting for peer1 to receive data")
	}

	// Проверяем список активных пиров
	activePeers1 := connector1.GetActivePeers()
	if len(activePeers1) != 1 {
		t.Fatalf("Connector1: expected 1 active peer, got %d", len(activePeers1))
	}

	activePeers2 := connector2.GetActivePeers()
	if len(activePeers2) != 1 {
		t.Fatalf("Connector2: expected 1 active peer, got %d", len(activePeers2))
	}

	t.Log("✓ Active peers count is correct")

	// Отключаемся
	t.Log("Disconnecting peers...")
	if err := connector1.Disconnect(peerID2); err != nil {
		t.Fatalf("Disconnect failed: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	// Проверяем что соединения закрыты
	activePeers1 = connector1.GetActivePeers()
	if len(activePeers1) != 0 {
		t.Fatalf("Connector1: expected 0 active peers after disconnect, got %d", len(activePeers1))
	}

	t.Log("✓ WebRTC P2P connection test passed!")
}

// TestWebRTCSimultaneousConnect тестирует случай когда оба пира одновременно инициируют подключение
func TestWebRTCSimultaneousConnect(t *testing.T) {
	// Запускаем router сервер
	addr := "localhost:18081"
	go func() {
		if err := router.Run(addr); err != nil {
			t.Logf("Router server error: %v", err)
		}
	}()

	time.Sleep(100 * time.Millisecond)

	// Создаем два пира
	pubkey1, privkey1, _ := ed25519.GenerateKey(nil)
	pubkey2, privkey2, _ := ed25519.GenerateKey(nil)

	peerID1 := router.PeerID{}
	peerID2 := router.PeerID{}
	copy(peerID1[:], pubkey1)
	copy(peerID2[:], pubkey2)

	t.Logf("Peer1 ID: %s", hex.EncodeToString(peerID1[:]))
	t.Logf("Peer2 ID: %s", hex.EncodeToString(peerID2[:]))

	// Подключаем к router
	client1 := router.NewClient(pubkey1, privkey1)
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	income1, _ := client1.Dial(ctx1, addr)

	client2 := router.NewClient(pubkey2, privkey2)
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	income2, _ := client2.Dial(ctx2, addr)

	// Создаем коннекторы
	cfg := ConnectorConfig{
		STUNServers: []string{"stun:stun.l.google.com:19302"},
	}
	connector1, err := NewConnector(client1, cfg, income1, privkey1)
	if err != nil {
		t.Fatalf("Failed to create connector1: %v", err)
	}
	connector2, err := NewConnector(client2, cfg, income2, privkey2)
	if err != nil {
		t.Fatalf("Failed to create connector2: %v", err)
	}

	peer1Connected := make(chan struct{})
	peer2Connected := make(chan struct{})
	connectionAttempts1 := 0
	connectionAttempts2 := 0

	// Обработчики событий
	go func() {
		for event := range connector1.Events() {
			switch event.Type {
			case EventConnected:
				t.Logf("Peer1: Connected")
				close(peer1Connected)
			case EventConnectionFailed:
				connectionAttempts1++
				t.Logf("Peer1: Connection attempt failed (may be expected in simultaneous connect)")
			}
		}
	}()

	go func() {
		for event := range connector2.Events() {
			switch event.Type {
			case EventConnected:
				t.Logf("Peer2: Connected")
				close(peer2Connected)
			case EventConnectionFailed:
				connectionAttempts2++
				t.Logf("Peer2: Connection attempt failed (may be expected in simultaneous connect)")
			}
		}
	}()

	// ОБА пира одновременно инициируют подключение
	t.Log("Both peers initiating connection simultaneously...")
	hexID1 := hex.EncodeToString(peerID1[:])
	hexID2 := hex.EncodeToString(peerID2[:])

	go connector1.Connect(hexID2)
	go connector2.Connect(hexID1)

	// Ждем соединения
	timeout := time.After(30 * time.Second)

	select {
	case <-peer1Connected:
		t.Log("Peer1: Connected")
	case <-timeout:
		t.Fatal("Timeout waiting for peer1 connection")
	}

	select {
	case <-peer2Connected:
		t.Log("Peer2: Connected")
	case <-timeout:
		t.Fatal("Timeout waiting for peer2 connection")
	}

	// Проверяем что установлено ровно одно соединение (не два)
	activePeers1 := connector1.GetActivePeers()
	activePeers2 := connector2.GetActivePeers()

	if len(activePeers1) != 1 || len(activePeers2) != 1 {
		t.Fatalf("Expected 1 connection on each side, got %d and %d", len(activePeers1), len(activePeers2))
	}

	t.Logf("✓ Simultaneous connect resolved correctly (connection attempts: peer1=%d, peer2=%d)",
		connectionAttempts1, connectionAttempts2)

	// Проверяем что можем отправлять данные
	time.Sleep(500 * time.Millisecond)

	peer, _ := connector1.GetPeer(peerID2)
	if err := peer.Send([]byte("test")); err != nil {
		t.Fatalf("Send failed after simultaneous connect: %v", err)
	}

	t.Log("✓ Simultaneous connect test passed!")
}

// BenchmarkWebRTCThroughput измеряет пропускную способность WebRTC DataChannel
func BenchmarkWebRTCThroughput(b *testing.B) {
	// Запускаем router сервер
	addr := "localhost:18082"
	go func() {
		router.Run(addr)
	}()
	time.Sleep(100 * time.Millisecond)

	// Создаем два пира
	pubkey1, privkey1, _ := ed25519.GenerateKey(nil)
	pubkey2, privkey2, _ := ed25519.GenerateKey(nil)

	peerID1 := router.PeerID{}
	peerID2 := router.PeerID{}
	copy(peerID1[:], pubkey1)
	copy(peerID2[:], pubkey2)

	// Подключаем к router
	client1 := router.NewClient(pubkey1, privkey1)
	ctx1, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	income1, _ := client1.Dial(ctx1, addr)

	client2 := router.NewClient(pubkey2, privkey2)
	ctx2, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	income2, _ := client2.Dial(ctx2, addr)

	// Создаем коннекторы
	cfg := ConnectorConfig{
		STUNServers: []string{"stun:stun.l.google.com:19302"},
	}
	connector1, err := NewConnector(client1, cfg, income1, privkey1)
	if err != nil {
		b.Fatalf("Failed to create connector1: %v", err)
	}
	connector2, err := NewConnector(client2, cfg, income2, privkey2)
	if err != nil {
		b.Fatalf("Failed to create connector2: %v", err)
	}

	connected := make(chan struct{})
	receivedCount := 0

	go func() {
		for event := range connector1.Events() {
			if event.Type == EventConnected {
				close(connected)
			}
		}
	}()

	go func() {
		for event := range connector2.Events() {
			if event.Type == EventDataReceived {
				receivedCount++
			}
		}
	}()

	// Устанавливаем соединение
	hexID2 := hex.EncodeToString(peerID2[:])
	connector1.Connect(hexID2)

	<-connected
	time.Sleep(1 * time.Second) // Даем время DataChannel открыться

	peer, _ := connector1.GetPeer(peerID2)

	// Бенчмарк
	payload := make([]byte, 1024) // 1KB
	b.SetBytes(int64(len(payload)))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		if err := peer.Send(payload); err != nil {
			b.Fatal(err)
		}
	}

	b.StopTimer()
	time.Sleep(1 * time.Second) // Ждем доставки

	fmt.Printf("\nReceived %d/%d messages (%.1f%%)\n",
		receivedCount, b.N, float64(receivedCount)/float64(b.N)*100)
}
