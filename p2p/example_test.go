package p2p_test

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"log"

	"sendy/p2p"
	"sendy/router"
)

func Example() {
	// Генерируем ключи
	pubkey, privkey, _ := ed25519.GenerateKey(nil)

	// Создаем клиента и подключаемся к router серверу
	client := router.NewClient(pubkey, privkey)
	ctx := context.Background()
	income, err := client.Dial(ctx, "localhost:8080")
	if err != nil {
		log.Fatal(err)
	}

	// Создаем WebRTC Connector
	cfg := p2p.ConnectorConfig{
		STUNServers: []string{"stun:stun.l.google.com:19302"},
	}
	connector, err := p2p.NewConnector(client, cfg, income, privkey)
	if err != nil {
		log.Fatal(err)
	}

	// Обрабатываем события в отдельной горутине
	go func() {
		for event := range connector.Events() {
			switch event.Type {
			case p2p.EventConnected:
				fmt.Printf("Connected to peer: %s\n", hex.EncodeToString(event.PeerID[:]))
				// Теперь можно отправлять данные
				event.Peer.Send([]byte("Hello!"))

			case p2p.EventDisconnected:
				fmt.Printf("Disconnected from peer: %s\n", hex.EncodeToString(event.PeerID[:]))

			case p2p.EventConnectionFailed:
				fmt.Printf("Connection failed: %v\n", event.Error)

			case p2p.EventDataReceived:
				fmt.Printf("Data from %s: %s\n", hex.EncodeToString(event.PeerID[:]), string(event.Data))

			case p2p.EventError:
				fmt.Printf("Error: %v\n", event.Error)
			}
		}
	}()

	// Устанавливаем соединение с удаленным пиром (асинхронно)
	remotePeerHex := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := connector.Connect(remotePeerHex); err != nil {
		log.Fatal(err)
	}

	// Или получаем существующее соединение
	if peer, ok := connector.GetPeer(router.PeerID{}); ok {
		peer.Send([]byte("test message"))
	}

	// Список активных пиров
	activePeers := connector.GetActivePeers()
	fmt.Printf("Active peers: %d\n", len(activePeers))

	// Отключение от конкретного пира
	// connector.Disconnect(peerID)

	// Отключение от всех пиров
	// connector.DisconnectAll()
}
