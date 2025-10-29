// Package p2p предоставляет WebRTC P2P коннектор для прямого обмена данными между пирами.
//
// Архитектура:
//
//  1. Connector управляет всеми WebRTC соединениями и использует router.Client для сигнализации
//     (обмен SDP offer/answer между пирами)
//
// 2. Connector.Connect(hexID) инициирует подключение к удаленному пиру (асинхронно):
//   - Создает WebRTC PeerConnection
//   - Генерирует SDP offer
//   - Отправляет offer через router
//   - Ждет SDP answer от удаленного пира
//   - Устанавливает WebRTC соединение
//
// 3. handleIncoming обрабатывает входящие сообщения от router.Client:
//   - SDP offers от других пиров (входящие подключения)
//   - SDP answers на наши offers (ответы на исходящие подключения)
//   - Разрешает коллизии при одновременном подключении (perfect negotiation)
//
// 4. События отправляются через канал Events():
//
//   - EventConnected - соединение установлено
//
//   - EventDisconnected - соединение разорвано
//
//   - EventConnectionFailed - не удалось установить соединение
//
//   - EventDataReceived - получены данные от пира
//
//   - EventError - ошибка на существующем соединении
//
//     5. После установки соединения данные передаются напрямую через WebRTC DataChannel,
//     без участия router сервера (P2P)
package p2p

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/udisondev/sendy/router"

	"github.com/pion/webrtc/v4"
)

var ErrInvalidIDFormat = errors.New("invalid id format")
var ErrConnectionTimeout = errors.New("connection timeout")
var ErrDecryptionFailed = errors.New("decryption failed")

// EncryptedMessage представляет зашифрованное сообщение с ключом отправителя
type EncryptedMessage struct {
	SenderEncPubKey [32]byte `json:"sender_enc_pubkey"` // Curve25519 публичный ключ отправителя
	EncryptedData   []byte   `json:"encrypted_data"`    // Зашифрованный payload
}

// EventType определяет тип события
type EventType uint8

const (
	EventConnected EventType = iota
	EventDisconnected
	EventConnectionFailed
	EventError
	EventDataReceived
)

// Event представляет событие от Connector
type Event struct {
	Type   EventType
	PeerID router.PeerID
	Peer   *Peer
	Data   []byte
	Error  error
}

// Connector управляет WebRTC соединениями
type Connector struct {
	cli           *router.Client
	config        webrtc.Configuration
	events        chan Event
	peers         sync.Map // map[router.PeerID]*Peer
	pendingOffers sync.Map // map[router.PeerID]chan router.ServerMessage
	blacklist     sync.Map // map[router.PeerID]struct{}
	peerEncKeys   sync.Map // map[router.PeerID]*Curve25519PublicKey - encryption keys received from peers

	// Ключи шифрования (выведены из Ed25519)
	encPubKey  *Curve25519PublicKey
	encPrivKey *Curve25519PrivateKey
	edPrivKey  ed25519.PrivateKey

	// SECURITY: Rate limiting для защиты от DoS
	offerCount sync.Map // map[router.PeerID]*offerCounter
}

// offerCounter отслеживает количество offer'ов от пира для rate limiting
type offerCounter struct {
	count      int
	lastReset  time.Time
	mu         sync.Mutex
}

const (
	maxOffersPerMinute = 10 // Максимум 10 offer'ов в минуту от одного пира
)

// Peer представляет WebRTC соединение с удаленным пиром
type Peer struct {
	ID          router.PeerID
	conn        *webrtc.PeerConnection
	dataChannel *webrtc.DataChannel
	connector   *Connector
	mu          sync.Mutex
}

// ConnectorConfig конфигурация для Connector
type ConnectorConfig struct {
	STUNServers []string
}

// NewConnector creates a new Connector instance
func NewConnector(cli *router.Client, cfg ConnectorConfig, income <-chan router.ServerMessage, edPrivKey ed25519.PrivateKey) (*Connector, error) {
	slog.Info("Creating P2P Connector", "stunServers", len(cfg.STUNServers))

	// Derive encryption keys from Ed25519 keys
	encPubKey, encPrivKey, err := DeriveEncryptionKeys(edPrivKey)
	if err != nil {
		slog.Error("Failed to derive encryption keys", "error", err)
		return nil, fmt.Errorf("derive encryption keys: %w", err)
	}
	slog.Info("Derived encryption keys for P2P", "pubKey", hex.EncodeToString(encPubKey[:8])+"...")

	config := webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{},
	}

	if len(cfg.STUNServers) > 0 {
		config.ICEServers = append(config.ICEServers, webrtc.ICEServer{
			URLs: cfg.STUNServers,
		})
		slog.Debug("Configured STUN servers", "urls", cfg.STUNServers)
	}

	c := &Connector{
		cli:        cli,
		config:     config,
		events:     make(chan Event, 100),
		encPubKey:  encPubKey,
		encPrivKey: encPrivKey,
		edPrivKey:  edPrivKey,
	}

	// Start incoming message handler
	go c.handleIncoming(income)
	slog.Debug("Started incoming message handler")

	return c, nil
}

// Events возвращает канал событий
func (c *Connector) Events() <-chan Event {
	return c.events
}

// encryptMessageForPeer шифрует сообщение для конкретного пира
// Возвращает JSON с envelope (EncryptedMessage)
// SECURITY: ВСЕ сообщения должны быть зашифрованы. Если у нас нет ключа пира - ошибка.
func (c *Connector) encryptMessageForPeer(peerID router.PeerID, payload []byte) ([]byte, error) {
	// SECURITY: Проверяем есть ли у нас ключ шифрования этого пира
	peerEncKeyVal, hasPeerKey := c.peerEncKeys.Load(peerID)
	if !hasPeerKey {
		// Нет ключа - это ошибка! Сначала должен быть KEY_EXCHANGE
		return nil, fmt.Errorf("no encryption key for peer - key exchange required first")
	}

	var envelope EncryptedMessage
	copy(envelope.SenderEncPubKey[:], (*c.encPubKey)[:])

	// Шифруем сообщение
	peerEncKey := peerEncKeyVal.(*Curve25519PublicKey)
	encrypted, err := EncryptMessage(payload, peerEncKey, c.encPrivKey)
	if err != nil {
		return nil, fmt.Errorf("encrypt: %w", err)
	}
	envelope.EncryptedData = encrypted
	slog.Debug("Encrypted message for peer",
		"peerID", hex.EncodeToString(peerID[:8])+"...",
		"originalSize", len(payload),
		"encryptedSize", len(encrypted))

	// Кодируем envelope в JSON
	return json.Marshal(envelope)
}

// sendKeyExchange отправляет сообщение обмена ключами
// SECURITY: Подписываем KEY_EXCHANGE чтобы предотвратить MITM на первом обмене ключами
func (c *Connector) sendKeyExchange(peerID router.PeerID) error {
	var envelope EncryptedMessage
	copy(envelope.SenderEncPubKey[:], (*c.encPubKey)[:])
	// Payload - просто маркер обмена ключами
	envelope.EncryptedData = []byte("KEY_EXCHANGE_V1")

	envelopeJSON, err := json.Marshal(envelope)
	if err != nil {
		return fmt.Errorf("marshal envelope: %w", err)
	}

	// SECURITY: Подписываем KEY_EXCHANGE нашим Ed25519 приватным ключом
	// Получатель проверит подпись используя наш PeerID (Ed25519 публичный ключ)
	signature := SignMessage(envelopeJSON, c.edPrivKey)
	signedMsg := SignedMessage{
		Payload:   envelopeJSON,
		Signature: signature,
	}
	signedMsgJSON, err := json.Marshal(signedMsg)
	if err != nil {
		return fmt.Errorf("marshal signed key exchange: %w", err)
	}

	slog.Info("Sending signed key exchange",
		"peerID", hex.EncodeToString(peerID[:8])+"...",
		"myEncKey", hex.EncodeToString(c.encPubKey[:8])+"...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err = c.cli.Send(ctx, peerID, signedMsgJSON)
	return err
}

// decryptMessageFromPeer расшифровывает сообщение от пира
// Извлекает ключ шифрования пира из envelope и сохраняет его
// Возвращает расшифрованный payload
func (c *Connector) decryptMessageFromPeer(peerID router.PeerID, envelopeJSON []byte) ([]byte, error) {
	// Декодируем envelope
	var envelope EncryptedMessage
	if err := json.Unmarshal(envelopeJSON, &envelope); err != nil {
		return nil, fmt.Errorf("unmarshal envelope: %w", err)
	}

	// SECURITY: Проверяем ключ шифрования пира (TOFU - Trust On First Use)
	newPeerEncKey := &Curve25519PublicKey{}
	copy((*newPeerEncKey)[:], envelope.SenderEncPubKey[:])

	// Проверяем, есть ли уже сохраненный ключ для этого пира
	if existingKeyVal, exists := c.peerEncKeys.Load(peerID); exists {
		existingKey := existingKeyVal.(*Curve25519PublicKey)
		// SECURITY: Ключ не должен меняться! Если изменился - это атака!
		if *existingKey != *newPeerEncKey {
			slog.Error("SECURITY ALERT: Peer encryption key changed!",
				"peerID", hex.EncodeToString(peerID[:8])+"...",
				"oldKey", hex.EncodeToString(existingKey[:8])+"...",
				"newKey", hex.EncodeToString(newPeerEncKey[:8])+"...")
			return nil, fmt.Errorf("peer encryption key changed - possible MITM attack")
		}
	} else {
		// Первый раз видим этот ключ - сохраняем (Trust On First Use)
		c.peerEncKeys.Store(peerID, newPeerEncKey)
		slog.Info("Stored peer encryption key (TOFU)",
			"peerID", hex.EncodeToString(peerID[:8])+"...",
			"encKey", hex.EncodeToString(newPeerEncKey[:8])+"...")
	}

	peerEncKey := newPeerEncKey

	// SECURITY: Проверяем тип сообщения
	// KEY_EXCHANGE - единственное разрешенное незашифрованное сообщение
	isKeyExchange := string(envelope.EncryptedData) == "KEY_EXCHANGE_V1"

	if isKeyExchange {
		// Это сообщение обмена ключами
		slog.Info("Received key exchange from peer",
			"peerID", hex.EncodeToString(peerID[:8])+"...",
			"peerEncKey", hex.EncodeToString(peerEncKey[:8])+"...")

		// KEY_EXCHANGE не содержит полезного payload - просто сигнал что ключ обменян
		return nil, nil  // nil payload означает "только обмен ключами"
	}

	// Все остальные сообщения ДОЛЖНЫ быть зашифрованы
	// Минимальная длина зашифрованного сообщения = 24 байта (nonce) + 16 байт (auth tag)
	if len(envelope.EncryptedData) < 40 {
		slog.Error("SECURITY ALERT: Received short unencrypted message (not KEY_EXCHANGE)!",
			"peerID", hex.EncodeToString(peerID[:8])+"...",
			"length", len(envelope.EncryptedData))
		return nil, fmt.Errorf("unencrypted non-KEY_EXCHANGE message - potential attack")
	}

	// Расшифровываем сообщение
	decrypted, err := DecryptMessage(envelope.EncryptedData, peerEncKey, c.encPrivKey)
	if err != nil {
		// SECURITY: Не расшифровалось - отклоняем
		slog.Warn("Decryption failed, rejecting message",
			"peerID", hex.EncodeToString(peerID[:8])+"...",
			"error", err)
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	slog.Debug("Decrypted message from peer",
		"peerID", hex.EncodeToString(peerID[:8])+"...",
		"encryptedSize", len(envelope.EncryptedData),
		"decryptedSize", len(decrypted))

	return decrypted, nil
}

// encryptDataChannelMessage шифрует сообщение для отправки через data channel
// Используется более простой формат без JSON envelope (только сырые байты)
func (c *Connector) encryptDataChannelMessage(peerID router.PeerID, data []byte) ([]byte, error) {
	// Получаем ключ шифрования пира
	peerEncKeyVal, ok := c.peerEncKeys.Load(peerID)
	if !ok {
		return nil, fmt.Errorf("peer encryption key not found")
	}

	peerEncKey := peerEncKeyVal.(*Curve25519PublicKey)

	// Шифруем данные
	encrypted, err := EncryptMessage(data, peerEncKey, c.encPrivKey)
	if err != nil {
		return nil, fmt.Errorf("encrypt: %w", err)
	}

	return encrypted, nil
}

// decryptDataChannelMessage расшифровывает сообщение полученное через data channel
func (c *Connector) decryptDataChannelMessage(peerID router.PeerID, encrypted []byte) ([]byte, error) {
	// Получаем ключ шифрования пира
	peerEncKeyVal, ok := c.peerEncKeys.Load(peerID)
	if !ok {
		return nil, fmt.Errorf("peer encryption key not found")
	}

	peerEncKey := peerEncKeyVal.(*Curve25519PublicKey)

	// Расшифровываем данные
	decrypted, err := DecryptMessage(encrypted, peerEncKey, c.encPrivKey)
	if err != nil {
		return nil, fmt.Errorf("decrypt: %w", err)
	}

	return decrypted, nil
}

// GetPeer возвращает установленное соединение с пиром
func (c *Connector) GetPeer(peerID router.PeerID) (*Peer, bool) {
	val, ok := c.peers.Load(peerID)
	if !ok {
		return nil, false
	}
	return val.(*Peer), true
}

// GetPeerByHex возвращает установленное соединение с пиром по hex ID
func (c *Connector) GetPeerByHex(hexID string) (*Peer, error) {
	peerIDBytes, err := hex.DecodeString(hexID)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidIDFormat, err)
	}

	if len(peerIDBytes) != router.PeerIDSize {
		return nil, fmt.Errorf("%w: expected %d bytes, got %d", ErrInvalidIDFormat, router.PeerIDSize, len(peerIDBytes))
	}

	var peerID router.PeerID
	copy(peerID[:], peerIDBytes)

	peer, ok := c.GetPeer(peerID)
	if !ok {
		return nil, fmt.Errorf("peer not found")
	}
	return peer, nil
}

// Disconnect закрывает соединение с конкретным пиром
func (c *Connector) Disconnect(peerID router.PeerID) error {
	val, ok := c.peers.LoadAndDelete(peerID)
	if !ok {
		return fmt.Errorf("peer not found")
	}
	peer := val.(*Peer)
	return peer.Close()
}

// DisconnectAll закрывает все активные соединения
func (c *Connector) DisconnectAll() {
	c.peers.Range(func(key, value any) bool {
		peer := value.(*Peer)
		peer.Close()
		return true
	})
	c.peers = sync.Map{}
}

// GetActivePeers возвращает список ID всех активных пиров
func (c *Connector) GetActivePeers() []router.PeerID {
	var peers []router.PeerID
	c.peers.Range(func(key, value any) bool {
		peerID := key.(router.PeerID)
		peers = append(peers, peerID)
		return true
	})
	return peers
}

// AddToBlacklist добавляет пира в черный список и разрывает с ним соединение
func (c *Connector) AddToBlacklist(peerID router.PeerID) {
	c.blacklist.Store(peerID, struct{}{})
	// Разрываем существующее соединение если есть
	c.Disconnect(peerID)
}

// RemoveFromBlacklist удаляет пира из черного списка
func (c *Connector) RemoveFromBlacklist(peerID router.PeerID) {
	c.blacklist.Delete(peerID)
}

// IsBlacklisted проверяет находится ли пир в черном списке
func (c *Connector) IsBlacklisted(peerID router.PeerID) bool {
	_, ok := c.blacklist.Load(peerID)
	return ok
}

// GetBlacklist возвращает список всех заблокированных пиров
func (c *Connector) GetBlacklist() []router.PeerID {
	var blocked []router.PeerID
	c.blacklist.Range(func(key, value any) bool {
		peerID := key.(router.PeerID)
		blocked = append(blocked, peerID)
		return true
	})
	return blocked
}

// Connect инициирует WebRTC соединение с пиром по hex ID (асинхронно)
func (c *Connector) Connect(hexID string) error {
	slog.Info("Initiating P2P connection", "peerID", hexID[:16]+"...")

	// Парсим hex ID
	peerIDBytes, err := hex.DecodeString(hexID)
	if err != nil {
		slog.Error("Invalid peer ID format", "hexID", hexID[:16]+"...", "error", err)
		return fmt.Errorf("%w: %v", ErrInvalidIDFormat, err)
	}

	if len(peerIDBytes) != router.PeerIDSize {
		slog.Error("Invalid peer ID size", "expected", router.PeerIDSize, "got", len(peerIDBytes))
		return fmt.Errorf("%w: expected %d bytes, got %d", ErrInvalidIDFormat, router.PeerIDSize, len(peerIDBytes))
	}

	var peerID router.PeerID
	copy(peerID[:], peerIDBytes)

	// Проверяем черный список
	if c.IsBlacklisted(peerID) {
		slog.Warn("Attempted connection to blacklisted peer", "peerID", hexID[:16]+"...")
		return fmt.Errorf("peer is blacklisted")
	}

	// Проверяем что соединение еще не установлено
	if _, exists := c.peers.Load(peerID); exists {
		slog.Debug("Connection already exists", "peerID", hexID[:16]+"...")
		return fmt.Errorf("connection already exists")
	}

	slog.Debug("Starting async connection", "peerID", hexID[:16]+"...")
	// Запускаем подключение асинхронно
	go c.connectAsync(peerID)
	return nil
}

// connectAsync выполняет подключение в фоне
func (c *Connector) connectAsync(peerID router.PeerID) {
	hexID := hex.EncodeToString(peerID[:8])
	slog.Debug("Creating WebRTC peer connection", "peerID", hexID+"...")

	// Создаем PeerConnection
	peerConn, err := webrtc.NewPeerConnection(c.config)
	if err != nil {
		slog.Error("Failed to create peer connection", "peerID", hexID+"...", "error", err)
		c.events <- Event{
			Type:   EventConnectionFailed,
			PeerID: peerID,
			Error:  fmt.Errorf("create peer connection: %w", err),
		}
		return
	}
	slog.Debug("Peer connection created", "peerID", hexID+"...")

	peer := &Peer{
		ID:        peerID,
		conn:      peerConn,
		connector: c,
	}

	// Создаем DataChannel
	slog.Debug("Creating data channel", "peerID", hexID+"...")
	dataChannel, err := peerConn.CreateDataChannel("data", nil)
	if err != nil {
		slog.Error("Failed to create data channel", "peerID", hexID+"...", "error", err)
		peerConn.Close()
		c.events <- Event{
			Type:   EventConnectionFailed,
			PeerID: peerID,
			Error:  fmt.Errorf("create data channel: %w", err),
		}
		return
	}
	peer.dataChannel = dataChannel
	slog.Debug("Data channel created", "peerID", hexID+"...")

	// Настраиваем обработчики
	c.setupDataChannel(peer, dataChannel)
	c.setupConnectionHandlers(peer, peerConn)

	// Создаем offer
	offer, err := peerConn.CreateOffer(nil)
	if err != nil {
		peerConn.Close()
		c.events <- Event{
			Type:   EventConnectionFailed,
			PeerID: peerID,
			Error:  fmt.Errorf("create offer: %w", err),
		}
		return
	}

	if err := peerConn.SetLocalDescription(offer); err != nil {
		peerConn.Close()
		c.events <- Event{
			Type:   EventConnectionFailed,
			PeerID: peerID,
			Error:  fmt.Errorf("set local description: %w", err),
		}
		return
	}

	// Ждем сбор ICE candidates
	gatherComplete := webrtc.GatheringCompletePromise(peerConn)
	select {
	case <-gatherComplete:
	case <-time.After(5 * time.Second):
		peerConn.Close()
		c.events <- Event{
			Type:   EventConnectionFailed,
			PeerID: peerID,
			Error:  fmt.Errorf("ICE gathering timeout"),
		}
		return
	}

	// SECURITY: Сначала отправляем KEY_EXCHANGE для обмена ключами
	slog.Info("Sending KEY_EXCHANGE before SDP offer", "peerID", hexID+"...")
	if err := c.sendKeyExchange(peerID); err != nil {
		peerConn.Close()
		c.events <- Event{
			Type:   EventConnectionFailed,
			PeerID: peerID,
			Error:  fmt.Errorf("send key exchange: %w", err),
		}
		return
	}

	// Ждем получения ключа от пира (с таймаутом)
	timeout := time.After(5 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

waitForPeerKey:
	for {
		select {
		case <-timeout:
			slog.Error("Timeout waiting for peer key exchange", "peerID", hexID+"...")
			peerConn.Close()
			c.events <- Event{
				Type:   EventConnectionFailed,
				PeerID: peerID,
				Error:  fmt.Errorf("timeout waiting for peer key exchange"),
			}
			return
		case <-ticker.C:
			// Проверяем есть ли ключ пира
			if _, ok := c.peerEncKeys.Load(peerID); ok {
				slog.Info("Received peer encryption key", "peerID", hexID+"...")
				break waitForPeerKey
			}
		}
	}

	// Кодируем offer
	offerJSON, err := json.Marshal(peerConn.LocalDescription())
	if err != nil {
		peerConn.Close()
		c.events <- Event{
			Type:   EventConnectionFailed,
			PeerID: peerID,
			Error:  fmt.Errorf("marshal offer: %w", err),
		}
		return
	}

	// SECURITY: Теперь шифруем offer (у нас уже есть ключ пира!)
	encryptedOffer, err := c.encryptMessageForPeer(peerID, offerJSON)
	if err != nil {
		peerConn.Close()
		c.events <- Event{
			Type:   EventConnectionFailed,
			PeerID: peerID,
			Error:  fmt.Errorf("encrypt offer: %w", err),
		}
		return
	}

	// SECURITY: Подписываем зашифрованный offer нашим Ed25519 приватным ключом
	// Это предотвращает MITM атаки на сигнализацию
	signature := SignMessage(encryptedOffer, c.edPrivKey)
	signedMsg := SignedMessage{
		Payload:   encryptedOffer,
		Signature: signature,
	}
	signedMsgJSON, err := json.Marshal(signedMsg)
	if err != nil {
		peerConn.Close()
		c.events <- Event{
			Type:   EventConnectionFailed,
			PeerID: peerID,
			Error:  fmt.Errorf("marshal signed offer: %w", err),
		}
		return
	}
	slog.Debug("Sending signed encrypted offer", "peerID", hex.EncodeToString(peerID[:8])+"...")

	// Создаем канал для ответа
	answerChan := make(chan []byte, 1)
	c.pendingOffers.Store(peerID, answerChan)

	// Отправляем signed encrypted offer
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	respCh, err := c.cli.Send(ctx, peerID, signedMsgJSON)
	if err != nil {
		peerConn.Close()
		c.pendingOffers.Delete(peerID)
		c.events <- Event{
			Type:   EventConnectionFailed,
			PeerID: peerID,
			Error:  fmt.Errorf("send offer: %w", err),
		}
		return
	}

	// Ждем подтверждение от сервера
	select {
	case resp := <-respCh:
		if resp.Type != router.Success {
			peerConn.Close()
			c.pendingOffers.Delete(peerID)
			c.events <- Event{
				Type:   EventConnectionFailed,
				PeerID: peerID,
				Error:  fmt.Errorf("offer rejected: type=%v", resp.Type),
			}
			return
		}
	case <-time.After(10 * time.Second):
		peerConn.Close()
		c.pendingOffers.Delete(peerID)
		c.events <- Event{
			Type:   EventConnectionFailed,
			PeerID: peerID,
			Error:  ErrConnectionTimeout,
		}
		return
	case <-ctx.Done():
		peerConn.Close()
		c.pendingOffers.Delete(peerID)
		return
	}

	// Ждем answer
	select {
	case encryptedAnswer, ok := <-answerChan:
		if !ok {
			// Канал закрыт - наш offer был отменен из-за одновременного подключения
			// Другая сторона обработает входящий offer
			peerConn.Close()
			return
		}

		// Расшифровываем answer
		slog.Debug("Received encrypted answer, decrypting...", "peerID", hex.EncodeToString(peerID[:8])+"...")
		answerJSON, err := c.decryptMessageFromPeer(peerID, encryptedAnswer)
		if err != nil {
			peerConn.Close()
			c.events <- Event{
				Type:   EventConnectionFailed,
				PeerID: peerID,
				Error:  fmt.Errorf("decrypt answer: %w", err),
			}
			return
		}

		var answer webrtc.SessionDescription
		if err := json.Unmarshal(answerJSON, &answer); err != nil {
			peerConn.Close()
			c.events <- Event{
				Type:   EventConnectionFailed,
				PeerID: peerID,
				Error:  fmt.Errorf("unmarshal answer: %w", err),
			}
			return
		}

		if err := peerConn.SetRemoteDescription(answer); err != nil {
			peerConn.Close()
			c.events <- Event{
				Type:   EventConnectionFailed,
				PeerID: peerID,
				Error:  fmt.Errorf("set remote description: %w", err),
			}
			return
		}

		c.peers.Store(peerID, peer)

	case <-time.After(30 * time.Second):
		peerConn.Close()
		c.pendingOffers.Delete(peerID)
		c.events <- Event{
			Type:   EventConnectionFailed,
			PeerID: peerID,
			Error:  ErrConnectionTimeout,
		}
		return
	case <-ctx.Done():
		peerConn.Close()
		c.pendingOffers.Delete(peerID)
		return
	}
}

// setupConnectionHandlers настраивает обработчики состояния соединения
func (c *Connector) setupConnectionHandlers(peer *Peer, peerConn *webrtc.PeerConnection) {
	peerConn.OnConnectionStateChange(func(state webrtc.PeerConnectionState) {
		switch state {
		case webrtc.PeerConnectionStateConnected:
			c.events <- Event{
				Type:   EventConnected,
				PeerID: peer.ID,
				Peer:   peer,
			}
		case webrtc.PeerConnectionStateDisconnected, webrtc.PeerConnectionStateFailed, webrtc.PeerConnectionStateClosed:
			c.peers.Delete(peer.ID)
			c.events <- Event{
				Type:   EventDisconnected,
				PeerID: peer.ID,
			}
		}
	})
}

// setupDataChannel настраивает обработчики для DataChannel
func (c *Connector) setupDataChannel(peer *Peer, dc *webrtc.DataChannel) {
	hexID := hex.EncodeToString(peer.ID[:8])

	dc.OnOpen(func() {
		slog.Info("Data channel opened", "peerID", hexID+"...")
	})

	dc.OnMessage(func(msg webrtc.DataChannelMessage) {
		slog.Debug("Received encrypted data", "peerID", hexID+"...", "encryptedBytes", len(msg.Data))

		// Расшифровываем данные
		decrypted, err := c.decryptDataChannelMessage(peer.ID, msg.Data)
		if err != nil {
			slog.Error("Failed to decrypt data channel message",
				"peerID", hexID+"...",
				"error", err)
			c.events <- Event{
				Type:   EventError,
				PeerID: peer.ID,
				Error:  fmt.Errorf("decrypt data: %w", err),
			}
			return
		}

		slog.Debug("Decrypted data channel message",
			"peerID", hexID+"...",
			"decryptedBytes", len(decrypted))

		c.events <- Event{
			Type:   EventDataReceived,
			PeerID: peer.ID,
			Peer:   peer,
			Data:   decrypted,
		}
	})

	dc.OnClose(func() {
		slog.Info("Data channel closed", "peerID", hexID+"...")
		c.peers.Delete(peer.ID)
	})

	dc.OnError(func(err error) {
		// SCTP "User Initiated Abort" - это нормально при закрытии соединения
		slog.Debug("Data channel error (will reconnect)", "peerID", hexID+"...", "error", err)
		c.events <- Event{
			Type:   EventError,
			PeerID: peer.ID,
			Error:  err,
		}
	})
}

// Send отправляет данные пиру (с шифрованием)
func (p *Peer) Send(data []byte) error {
	hexID := hex.EncodeToString(p.ID[:8])
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.dataChannel == nil {
		slog.Error("Cannot send: data channel is nil", "peerID", hexID+"...")
		return fmt.Errorf("data channel is nil")
	}

	state := p.dataChannel.ReadyState()
	if state != webrtc.DataChannelStateOpen {
		slog.Warn("Cannot send: data channel not open", "peerID", hexID+"...", "state", state.String())
		return fmt.Errorf("data channel is not open: state=%v", state)
	}

	// Шифруем данные перед отправкой
	encrypted, err := p.connector.encryptDataChannelMessage(p.ID, data)
	if err != nil {
		slog.Error("Failed to encrypt data", "peerID", hexID+"...", "error", err)
		return fmt.Errorf("encrypt data: %w", err)
	}

	slog.Debug("Sending encrypted data",
		"peerID", hexID+"...",
		"originalBytes", len(data),
		"encryptedBytes", len(encrypted))

	return p.dataChannel.Send(encrypted)
}

// Close закрывает соединение с пиром
func (p *Peer) Close() error {
	hexID := hex.EncodeToString(p.ID[:8])
	slog.Info("Closing peer connection", "peerID", hexID+"...")

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.conn != nil {
		return p.conn.Close()
	}
	return nil
}

// handleIncoming обрабатывает входящие сообщения от router
func (c *Connector) handleIncoming(income <-chan router.ServerMessage) {
	for msg := range income {
		slog.Debug("Received message from peer",
			"from", hex.EncodeToString(msg.SenderID[:8])+"...")

		// ВАЖНО: Проверяем был ли у нас ключ от этого пира ДО расшифровки
		_, hadKeyBefore := c.peerEncKeys.Load(msg.SenderID)

		// SECURITY: Все сообщения теперь подписаны (включая KEY_EXCHANGE)
		var signedMsg SignedMessage
		if err := json.Unmarshal(msg.Payload, &signedMsg); err != nil {
			slog.Error("Failed to unmarshal SignedMessage",
				"from", hex.EncodeToString(msg.SenderID[:8])+"...",
				"error", err)
			c.events <- Event{
				Type:   EventError,
				PeerID: msg.SenderID,
				Error:  fmt.Errorf("invalid message format: %w", err),
			}
			continue
		}

		// SECURITY: Верифицируем Ed25519 подпись
		slog.Debug("Verifying Ed25519 signature",
			"from", hex.EncodeToString(msg.SenderID[:8])+"...")

		senderPubKey := ed25519.PublicKey(msg.SenderID[:])
		if !VerifySignature(signedMsg.Payload, signedMsg.Signature, senderPubKey) {
			slog.Error("SECURITY ALERT: Invalid Ed25519 signature!",
				"from", hex.EncodeToString(msg.SenderID[:8])+"...",
				"payloadSize", len(signedMsg.Payload),
				"signatureSize", len(signedMsg.Signature))
			c.events <- Event{
				Type:   EventError,
				PeerID: msg.SenderID,
				Error:  fmt.Errorf("invalid Ed25519 signature - potential MITM attack"),
			}
			continue
		}

		slog.Debug("Signature verified successfully",
			"from", hex.EncodeToString(msg.SenderID[:8])+"...")
		payloadToDecrypt := signedMsg.Payload

		// Расшифровываем сообщение
		decryptedPayload, err := c.decryptMessageFromPeer(msg.SenderID, payloadToDecrypt)
		if err != nil {
			c.events <- Event{
				Type:   EventError,
				PeerID: msg.SenderID,
				Error:  fmt.Errorf("decrypt incoming message: %w", err),
			}
			continue
		}

		// SECURITY: nil payload означает KEY_EXCHANGE (просто обмен ключами, нет данных)
		if decryptedPayload == nil {
			slog.Debug("KEY_EXCHANGE received",
				"from", hex.EncodeToString(msg.SenderID[:8])+"...")

			// ВАЖНО: Отправляем KEY_EXCHANGE обратно ТОЛЬКО если это ПЕРВЫЙ раз (не было ключа)
			// Это предотвращает бесконечный цикл KEY_EXCHANGE между пирами
			if !hadKeyBefore {
				// Первый раз видим ключ от этого пира - отправляем KEY_EXCHANGE в ответ
				if err := c.sendKeyExchange(msg.SenderID); err != nil {
					slog.Warn("Failed to send KEY_EXCHANGE response",
						"peerID", hex.EncodeToString(msg.SenderID[:8])+"...",
						"error", err)
				} else {
					slog.Info("Sent KEY_EXCHANGE response (first key exchange)",
						"to", hex.EncodeToString(msg.SenderID[:8])+"...")
				}
			} else {
				slog.Debug("KEY_EXCHANGE received (key already known, not responding)",
					"from", hex.EncodeToString(msg.SenderID[:8])+"...")
			}
			continue
		}

		// Парсим SessionDescription чтобы узнать тип
		var sdp webrtc.SessionDescription
		if err := json.Unmarshal(decryptedPayload, &sdp); err != nil {
			c.events <- Event{
				Type:   EventError,
				PeerID: msg.SenderID,
				Error:  fmt.Errorf("unmarshal session description: %w", err),
			}
			continue
		}

		switch sdp.Type {
		case webrtc.SDPTypeOffer:
			// Это входящий offer - обрабатываем как новое входящее соединение
			// Проверяем есть ли у нас pending offer к этому же пиру (одновременное подключение)
			if ch, ok := c.pendingOffers.Load(msg.SenderID); ok {
				// Оба пира одновременно инициировали соединение
				// Используем сравнение ID для выбора кто будет продолжать
				// Тот у кого ID больше - отменяет свой offer и принимает входящий
				// Это предотвращает создание двух соединений
				var ourID router.PeerID
				copy(ourID[:], c.cli.GetPublicKey())

				if compareIDs(ourID, msg.SenderID) > 0 {
					// Наш ID больше - отменяем наш offer и принимаем входящий
					c.pendingOffers.Delete(msg.SenderID)
					answerChan := ch.(chan []byte)
					close(answerChan)
					go c.handleIncomingOffer(msg.SenderID, decryptedPayload)
				}
				// Иначе игнорируем входящий offer - пусть другая сторона примет наш
				continue
			}

			// Обычный входящий offer
			go c.handleIncomingOffer(msg.SenderID, decryptedPayload)

		case webrtc.SDPTypeAnswer:
			// Это answer на наш offer
			if ch, ok := c.pendingOffers.LoadAndDelete(msg.SenderID); ok {
				answerChan := ch.(chan []byte)
				// Отправляем encrypted answer (после проверки подписи, будет расшифрован в connectAsync)
				select {
				case answerChan <- payloadToDecrypt:
				default:
				}
			}
			// Если нет pending offer - игнорируем (возможно уже обработали)

		default:
			c.events <- Event{
				Type:   EventError,
				PeerID: msg.SenderID,
				Error:  fmt.Errorf("unexpected SDP type: %v", sdp.Type),
			}
		}
	}
}

// compareIDs сравнивает два PeerID для выбора initiator при одновременном подключении
func compareIDs(a, b router.PeerID) int {
	for i := 0; i < len(a); i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	return 0
}

// checkOfferRateLimit проверяет rate limit для offer'ов от пира
func (c *Connector) checkOfferRateLimit(peerID router.PeerID) bool {
	now := time.Now()

	// Получаем или создаем counter для пира
	counterVal, _ := c.offerCount.LoadOrStore(peerID, &offerCounter{
		count: 0,
		lastReset: now,
	})
	counter := counterVal.(*offerCounter)

	counter.mu.Lock()
	defer counter.mu.Unlock()

	// Сбрасываем counter если прошла минута
	if now.Sub(counter.lastReset) > time.Minute {
		counter.count = 0
		counter.lastReset = now
	}

	// Проверяем лимит
	if counter.count >= maxOffersPerMinute {
		slog.Warn("SECURITY: Rate limit exceeded for peer",
			"peerID", hex.EncodeToString(peerID[:8])+"...",
			"count", counter.count,
			"limit", maxOffersPerMinute)
		return false
	}

	counter.count++
	return true
}

// handleIncomingOffer обрабатывает входящий offer от удаленного пира
func (c *Connector) handleIncomingOffer(peerID router.PeerID, offerJSON []byte) {
	// SECURITY: Проверяем rate limit
	if !c.checkOfferRateLimit(peerID) {
		slog.Warn("Rejecting offer due to rate limit", "peerID", hex.EncodeToString(peerID[:8])+"...")
		return
	}

	// Проверяем черный список
	if c.IsBlacklisted(peerID) {
		// Игнорируем подключения от заблокированных пиров
		return
	}

	// Проверяем что соединение еще не установлено или не устанавливается
	if _, exists := c.peers.Load(peerID); exists {
		return
	}
	if _, exists := c.pendingOffers.Load(peerID); exists {
		// Уже есть активная попытка подключения - не должно происходить
		// так как handleIncoming уже обработал этот случай
		return
	}

	// Парсим offer
	var offer webrtc.SessionDescription
	if err := json.Unmarshal(offerJSON, &offer); err != nil {
		c.events <- Event{
			Type:   EventError,
			PeerID: peerID,
			Error:  fmt.Errorf("unmarshal offer: %w", err),
		}
		return
	}

	// Создаем PeerConnection
	peerConn, err := webrtc.NewPeerConnection(c.config)
	if err != nil {
		c.events <- Event{
			Type:   EventConnectionFailed,
			PeerID: peerID,
			Error:  fmt.Errorf("create peer connection: %w", err),
		}
		return
	}

	peer := &Peer{
		ID:        peerID,
		conn:      peerConn,
		connector: c,
	}

	// Устанавливаем обработчик для входящего DataChannel
	peerConn.OnDataChannel(func(dc *webrtc.DataChannel) {
		peer.dataChannel = dc
		c.setupDataChannel(peer, dc)
	})

	// Настраиваем обработчики состояния
	c.setupConnectionHandlers(peer, peerConn)

	// Устанавливаем remote description (offer)
	if err := peerConn.SetRemoteDescription(offer); err != nil {
		peerConn.Close()
		c.events <- Event{
			Type:   EventConnectionFailed,
			PeerID: peerID,
			Error:  fmt.Errorf("set remote description: %w", err),
		}
		return
	}

	// Создаем answer
	answer, err := peerConn.CreateAnswer(nil)
	if err != nil {
		peerConn.Close()
		c.events <- Event{
			Type:   EventConnectionFailed,
			PeerID: peerID,
			Error:  fmt.Errorf("create answer: %w", err),
		}
		return
	}

	if err := peerConn.SetLocalDescription(answer); err != nil {
		peerConn.Close()
		c.events <- Event{
			Type:   EventConnectionFailed,
			PeerID: peerID,
			Error:  fmt.Errorf("set local description: %w", err),
		}
		return
	}

	// Ждем сбор ICE candidates
	gatherComplete := webrtc.GatheringCompletePromise(peerConn)
	select {
	case <-gatherComplete:
	case <-time.After(5 * time.Second):
		peerConn.Close()
		c.events <- Event{
			Type:   EventConnectionFailed,
			PeerID: peerID,
			Error:  fmt.Errorf("ICE gathering timeout"),
		}
		return
	}

	// Кодируем answer
	answerJSON, err := json.Marshal(peerConn.LocalDescription())
	if err != nil {
		peerConn.Close()
		c.events <- Event{
			Type:   EventConnectionFailed,
			PeerID: peerID,
			Error:  fmt.Errorf("marshal answer: %w", err),
		}
		return
	}

	// SECURITY: Проверяем есть ли у нас ключ пира (должен быть, т.к. offer был зашифрован)
	hexID := hex.EncodeToString(peerID[:8])
	if _, hasKey := c.peerEncKeys.Load(peerID); !hasKey {
		// Странно - offer был зашифрован, но ключа нет. Отправляем KEY_EXCHANGE
		slog.Warn("No peer key when sending answer, sending KEY_EXCHANGE", "peerID", hexID+"...")
		if err := c.sendKeyExchange(peerID); err != nil {
			peerConn.Close()
			c.events <- Event{
				Type:   EventConnectionFailed,
				PeerID: peerID,
				Error:  fmt.Errorf("send key exchange: %w", err),
			}
			return
		}
		// Ждем ключ с таймаутом
		timeout := time.After(5 * time.Second)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
	waitForKey:
		for {
			select {
			case <-timeout:
				peerConn.Close()
				c.events <- Event{
					Type:   EventConnectionFailed,
					PeerID: peerID,
					Error:  fmt.Errorf("timeout waiting for peer key"),
				}
				return
			case <-ticker.C:
				if _, ok := c.peerEncKeys.Load(peerID); ok {
					break waitForKey
				}
			}
		}
	}

	// Шифруем answer (теперь точно есть ключ)
	encryptedAnswer, err := c.encryptMessageForPeer(peerID, answerJSON)
	if err != nil {
		peerConn.Close()
		c.events <- Event{
			Type:   EventConnectionFailed,
			PeerID: peerID,
			Error:  fmt.Errorf("encrypt answer: %w", err),
		}
		return
	}

	// SECURITY: Подписываем зашифрованный answer нашим Ed25519 приватным ключом
	signature := SignMessage(encryptedAnswer, c.edPrivKey)
	signedMsg := SignedMessage{
		Payload:   encryptedAnswer,
		Signature: signature,
	}
	signedMsgJSON, err := json.Marshal(signedMsg)
	if err != nil {
		peerConn.Close()
		c.events <- Event{
			Type:   EventConnectionFailed,
			PeerID: peerID,
			Error:  fmt.Errorf("marshal signed answer: %w", err),
		}
		return
	}
	slog.Debug("Sending signed encrypted answer", "peerID", hex.EncodeToString(peerID[:8])+"...")

	// Отправляем signed encrypted answer
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	respCh, err := c.cli.Send(ctx, peerID, signedMsgJSON)
	if err != nil {
		peerConn.Close()
		c.events <- Event{
			Type:   EventConnectionFailed,
			PeerID: peerID,
			Error:  fmt.Errorf("send answer: %w", err),
		}
		return
	}

	// Ждем подтверждение
	select {
	case resp := <-respCh:
		if resp.Type == router.Success {
			c.peers.Store(peerID, peer)
		} else {
			peerConn.Close()
			c.events <- Event{
				Type:   EventConnectionFailed,
				PeerID: peerID,
				Error:  fmt.Errorf("answer rejected: type=%v", resp.Type),
			}
		}
	case <-time.After(10 * time.Second):
		peerConn.Close()
		c.events <- Event{
			Type:   EventConnectionFailed,
			PeerID: peerID,
			Error:  ErrConnectionTimeout,
		}
	case <-ctx.Done():
		peerConn.Close()
	}
}
