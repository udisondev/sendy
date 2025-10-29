package chat

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"sendy/p2p"
	"sendy/router"
)

// ChatEvent represents a chat event
type ChatEvent struct {
	Type         ChatEventType
	PeerID       router.PeerID
	Message      *Message
	Contact      *Contact
	FileTransfer *FileTransfer
	Error        error
}

// ChatEventType defines chat event type
type ChatEventType uint8

const (
	ChatEventMessageReceived ChatEventType = iota
	ChatEventMessageSent
	ChatEventContactAdded
	ChatEventContactOnline
	ChatEventContactOffline
	ChatEventConnectionFailed
	ChatEventError
	ChatEventFileTransferStarted
	ChatEventFileTransferProgress
	ChatEventFileTransferCompleted
	ChatEventFileTransferFailed
)

type Chat struct {
	connector       *p2p.Connector
	storage         *Storage
	fileTransferMgr *FileTransferManager
	events          chan ChatEvent
	mu              sync.Mutex
}

// NewChat creates a new chat instance
func NewChat(connector *p2p.Connector, storage *Storage, dataDir string) *Chat {
	slog.Info("Creating chat instance")

	c := &Chat{
		connector:       connector,
		storage:         storage,
		fileTransferMgr: NewFileTransferManager(storage, dataDir),
		events:          make(chan ChatEvent, 100),
	}

	// Start connector events handler
	go c.handleConnectorEvents()
	slog.Debug("Started connector events handler")

	// Start auto-reconnect job
	go c.autoReconnect()
	slog.Debug("Started auto-reconnect job")

	return c
}

// Events returns chat events channel
func (c *Chat) Events() <-chan ChatEvent {
	return c.events
}

// handleConnectorEvents handles events from p2p.Connector
func (c *Chat) handleConnectorEvents() {
	slog.Debug("Connector events handler started")
	for event := range c.connector.Events() {
		hexID := hex.EncodeToString(event.PeerID[:8])

		switch event.Type {
		case p2p.EventConnected:
			slog.Info("Peer connected", "peerID", hexID+"...")

			// Check if this peer is in our contacts
			contact, err := c.storage.GetContact(event.PeerID)
			if err != nil || contact == nil {
				// Contact not found - automatically add on connection
				slog.Info("Auto-adding new contact on connection", "peerID", hexID+"...")
				contactName := hex.EncodeToString(event.PeerID[:8]) + "..."

				if err := c.storage.AddContact(event.PeerID, contactName); err != nil {
					slog.Error("Failed to auto-add contact", "peerID", hexID+"...", "error", err)
				} else {
					slog.Info("Contact auto-added successfully", "peerID", hexID+"...", "name", contactName)
					// Send event about new contact
					newContact := &Contact{
						PeerID: event.PeerID,
						Name:   contactName,
					}
					c.events <- ChatEvent{
						Type:    ChatEventContactAdded,
						PeerID:  event.PeerID,
						Contact: newContact,
					}
				}
			}

			// Update last activity time
			c.storage.UpdateLastSeen(event.PeerID)

			c.events <- ChatEvent{
				Type:   ChatEventContactOnline,
				PeerID: event.PeerID,
			}

		case p2p.EventDisconnected:
			slog.Info("Peer disconnected", "peerID", hexID+"...")
			c.events <- ChatEvent{
				Type:   ChatEventContactOffline,
				PeerID: event.PeerID,
			}

		case p2p.EventDataReceived:
			slog.Debug("Received message from peer", "peerID", hexID+"...", "length", len(event.Data))

			// Check if sender is in our contacts
			contact, err := c.storage.GetContact(event.PeerID)
			if err != nil || contact == nil {
				// Contact not found - automatically add
				slog.Info("Auto-adding new contact from incoming message", "peerID", hexID+"...")
				contactName := hex.EncodeToString(event.PeerID[:8]) + "..."

				if err := c.storage.AddContact(event.PeerID, contactName); err != nil {
					slog.Error("Failed to auto-add contact", "peerID", hexID+"...", "error", err)
				} else {
					slog.Info("Contact auto-added successfully", "peerID", hexID+"...", "name", contactName)
					// Send event about new contact
					newContact := &Contact{
						PeerID: event.PeerID,
						Name:   contactName,
					}
					c.events <- ChatEvent{
						Type:    ChatEventContactAdded,
						PeerID:  event.PeerID,
						Contact: newContact,
					}
				}
			}

			// Check if this is a file transfer message or regular message
			var ftMsg FileTransferMessage
			if err := json.Unmarshal(event.Data, &ftMsg); err == nil && ftMsg.TransferID != "" {
				// This is a file transfer message
				slog.Debug("Received file transfer message", "peerID", hexID+"...", "type", ftMsg.Type, "transferID", ftMsg.TransferID)
				c.handleFileTransferMessage(event.PeerID, &ftMsg)
				continue
			}

			// Regular text message
			msg := &Message{
				PeerID:     event.PeerID,
				Content:    string(event.Data),
				Timestamp:  time.Now(),
				IsOutgoing: false,
				IsRead:     false,
			}

			if err := c.storage.SaveMessage(msg); err != nil {
				slog.Error("Failed to save received message", "peerID", hexID+"...", "error", err)
				c.events <- ChatEvent{
					Type:  ChatEventError,
					Error: fmt.Errorf("save message: %w", err),
				}
				continue
			}

			c.storage.UpdateLastSeen(event.PeerID)
			slog.Debug("Message saved to storage", "peerID", hexID+"...")

			c.events <- ChatEvent{
				Type:    ChatEventMessageReceived,
				PeerID:  event.PeerID,
				Message: msg,
			}

		case p2p.EventConnectionFailed:
			slog.Error("Connection failed", "peerID", hexID+"...", "error", event.Error)
			c.events <- ChatEvent{
				Type:   ChatEventConnectionFailed,
				PeerID: event.PeerID,
				Error:  event.Error,
			}

		case p2p.EventError:
			slog.Error("P2P error", "peerID", hexID+"...", "error", event.Error)
			c.events <- ChatEvent{
				Type:   ChatEventError,
				PeerID: event.PeerID,
				Error:  event.Error,
			}
		}
	}
	slog.Info("Connector events handler stopped")
}

// SendMessage sends message to contact
func (c *Chat) SendMessage(peerID router.PeerID, content string) error {
	hexID := hex.EncodeToString(peerID[:8])
	slog.Debug("Sending message", "peerID", hexID+"...", "length", len(content))

	// Get peer
	peer, ok := c.connector.GetPeer(peerID)
	if !ok {
		slog.Warn("Cannot send message: peer not connected", "peerID", hexID+"...")
		return fmt.Errorf("peer not connected")
	}

	// Send
	if err := peer.Send([]byte(content)); err != nil {
		slog.Error("Failed to send message", "peerID", hexID+"...", "error", err)
		return fmt.Errorf("send: %w", err)
	}
	slog.Debug("Message sent via P2P", "peerID", hexID+"...")

	// Save to history
	msg := &Message{
		PeerID:     peerID,
		Content:    content,
		Timestamp:  time.Now(),
		IsOutgoing: true,
		IsRead:     true, // Outgoing messages immediately marked as read
	}

	if err := c.storage.SaveMessage(msg); err != nil {
		slog.Error("Failed to save sent message", "peerID", hexID+"...", "error", err)
		return fmt.Errorf("save message: %w", err)
	}
	slog.Debug("Sent message saved to storage", "peerID", hexID+"...")

	c.events <- ChatEvent{
		Type:    ChatEventMessageSent,
		PeerID:  peerID,
		Message: msg,
	}

	return nil
}

// Connect establishes connection with contact
func (c *Chat) Connect(hexID string) error {
	return c.connector.Connect(hexID)
}

// Disconnect terminates connection with contact
func (c *Chat) Disconnect(peerID router.PeerID) error {
	return c.connector.Disconnect(peerID)
}

// AddContact adds new contact
func (c *Chat) AddContact(hexID string, name string) error {
	slog.Info("Adding contact", "hexID", hexID[:16]+"...", "name", name)

	peerIDBytes, err := hex.DecodeString(hexID)
	if err != nil {
		slog.Error("Invalid contact hex ID", "hexID", hexID[:16]+"...", "error", err)
		return fmt.Errorf("invalid hex id: %w", err)
	}

	if len(peerIDBytes) != router.PeerIDSize {
		slog.Error("Invalid contact ID size", "expected", router.PeerIDSize, "got", len(peerIDBytes))
		return fmt.Errorf("invalid peer id size")
	}

	var peerID router.PeerID
	copy(peerID[:], peerIDBytes)

	if err := c.storage.AddContact(peerID, name); err != nil {
		slog.Error("Failed to add contact", "peerID", hexID[:16]+"...", "error", err)
		return err
	}

	slog.Info("Contact added successfully", "peerID", hexID[:16]+"...", "name", name)
	return nil
}

// BlockContact blocks contact and terminates connection
func (c *Chat) BlockContact(peerID router.PeerID) error {
	hexID := hex.EncodeToString(peerID[:8])
	slog.Info("Blocking contact", "peerID", hexID+"...")

	// Add to connector blacklist
	c.connector.AddToBlacklist(peerID)

	// Mark as blocked in database
	if err := c.storage.SetBlocked(peerID, true); err != nil {
		slog.Error("Failed to block contact", "peerID", hexID+"...", "error", err)
		return err
	}

	slog.Info("Contact blocked", "peerID", hexID+"...")
	return nil
}

// UnblockContact unblocks a contact
func (c *Chat) UnblockContact(peerID router.PeerID) error {
	hexID := hex.EncodeToString(peerID[:8])
	slog.Info("Unblocking contact", "peerID", hexID+"...")

	c.connector.RemoveFromBlacklist(peerID)

	if err := c.storage.SetBlocked(peerID, false); err != nil {
		slog.Error("Failed to unblock contact", "peerID", hexID+"...", "error", err)
		return err
	}

	slog.Info("Contact unblocked", "peerID", hexID+"...")
	return nil
}

// RenameContact renames a contact
func (c *Chat) RenameContact(peerID router.PeerID, newName string) error {
	return c.storage.UpdateContactName(peerID, newName)
}

// DeleteContact deletes a contact and all conversation history
func (c *Chat) DeleteContact(peerID router.PeerID) error {
	// Disconnect connection
	c.Disconnect(peerID)

	// Delete from database
	return c.storage.DeleteContact(peerID)
}

// GetContacts returns all contacts
func (c *Chat) GetContacts() ([]*Contact, error) {
	return c.storage.GetAllContacts()
}

// GetMessages returns messages with a contact
func (c *Chat) GetMessages(peerID router.PeerID, limit int) ([]*Message, error) {
	return c.storage.GetMessages(peerID, limit)
}

// SearchMessages searches for messages containing the query string across all contacts
func (c *Chat) SearchMessages(query string, limit int) ([]*SearchResult, error) {
	return c.storage.SearchMessages(query, limit)
}

// MarkAsRead marks messages as read
func (c *Chat) MarkAsRead(peerID router.PeerID) error {
	return c.storage.MarkAsRead(peerID)
}

// GetUnreadCount returns the number of unread messages
func (c *Chat) GetUnreadCount(peerID router.PeerID) (int, error) {
	return c.storage.GetUnreadCount(peerID)
}

// IsOnline checks if a contact is online
func (c *Chat) IsOnline(peerID router.PeerID) bool {
	_, ok := c.connector.GetPeer(peerID)
	return ok
}

// SendFile starts file sending to contact
func (c *Chat) SendFile(peerID router.PeerID, filePath string) error {
	hexID := hex.EncodeToString(peerID[:8])
	slog.Info("Starting file transfer", "peerID", hexID+"...", "file", filePath)

	// Check that peer is connected
	peer, ok := c.connector.GetPeer(peerID)
	if !ok {
		return fmt.Errorf("peer not connected")
	}

	// Start sending
	ft, err := c.fileTransferMgr.StartSending(peerID, filePath)
	if err != nil {
		return fmt.Errorf("start sending: %w", err)
	}

	// Save to database
	c.storage.SaveFileTransfer(ft.ID, peerID, ft.FileName, ft.FileSize, ft.FilePath, true, string(FileTransferPending))

	// Send START message
	startMsg := &FileTransferMessage{
		Type:        FileTransferStart,
		TransferID:  ft.ID,
		FileName:    ft.FileName,
		FileSize:    ft.FileSize,
		TotalChunks: ft.TotalChunks,
	}

	data, err := json.Marshal(startMsg)
	if err != nil {
		return fmt.Errorf("marshal start message: %w", err)
	}

	if err := peer.Send(data); err != nil {
		return fmt.Errorf("send start message: %w", err)
	}

	// Send event
	c.events <- ChatEvent{
		Type:         ChatEventFileTransferStarted,
		PeerID:       peerID,
		FileTransfer: ft,
	}

	// Start goroutine for sending chunks
	go c.sendFileChunks(peerID, ft)

	return nil
}

// sendFileChunks sends file chunks
func (c *Chat) sendFileChunks(peerID router.PeerID, ft *FileTransfer) {
	hexID := hex.EncodeToString(peerID[:8])
	slog.Debug("Starting to send file chunks", "peerID", hexID+"...", "transferID", ft.ID, "totalChunks", ft.TotalChunks)

	peer, ok := c.connector.GetPeer(peerID)
	if !ok {
		slog.Error("Peer disconnected during file transfer", "peerID", hexID+"...")
		c.handleFileTransferError(ft, fmt.Errorf("peer disconnected"))
		return
	}

	// Update status
	ft.Status = FileTransferTransferring
	c.storage.SaveFileTransfer(ft.ID, peerID, ft.FileName, ft.FileSize, ft.FilePath, true, string(FileTransferTransferring))

	// Read and send chunks
	buffer := make([]byte, ChunkSize)
	for chunkIndex := 0; chunkIndex < ft.TotalChunks; chunkIndex++ {
		n, err := ft.File.Read(buffer)
		if err != nil && n == 0 {
			slog.Error("Failed to read chunk", "peerID", hexID+"...", "transferID", ft.ID, "chunk", chunkIndex, "error", err)
			c.handleFileTransferError(ft, err)
			return
		}

		chunkMsg := &FileTransferMessage{
			Type:        FileTransferChunk,
			TransferID:  ft.ID,
			ChunkIndex:  chunkIndex,
			TotalChunks: ft.TotalChunks,
			Data:        buffer[:n],
		}

		data, err := json.Marshal(chunkMsg)
		if err != nil {
			slog.Error("Failed to marshal chunk", "error", err)
			c.handleFileTransferError(ft, err)
			return
		}

		if err := peer.Send(data); err != nil {
			slog.Error("Failed to send chunk", "peerID", hexID+"...", "transferID", ft.ID, "chunk", chunkIndex, "error", err)
			c.handleFileTransferError(ft, err)
			return
		}

		// Update progress
		ft.UpdateProgress(chunkIndex + 1)
		c.storage.UpdateFileTransferProgress(ft.ID, ft.Progress)

		// Send progress event every 10%
		if ft.Progress%10 == 0 {
			c.events <- ChatEvent{
				Type:         ChatEventFileTransferProgress,
				PeerID:       peerID,
				FileTransfer: ft,
			}
		}

		slog.Debug("Sent chunk", "peerID", hexID+"...", "transferID", ft.ID, "chunk", chunkIndex, "progress", ft.Progress)
	}

	// Calculate hash
	ft.File.Close()
	hash, err := CalculateFileHash(ft.FilePath)
	if err != nil {
		slog.Error("Failed to calculate file hash", "error", err)
		c.handleFileTransferError(ft, err)
		return
	}
	ft.Hash = hash

	// Send END message
	endMsg := &FileTransferMessage{
		Type:       FileTransferEnd,
		TransferID: ft.ID,
		SHA256Hash: hash,
	}

	data, err := json.Marshal(endMsg)
	if err != nil {
		slog.Error("Failed to marshal end message", "error", err)
		c.handleFileTransferError(ft, err)
		return
	}

	if err := peer.Send(data); err != nil {
		slog.Error("Failed to send end message", "error", err)
		c.handleFileTransferError(ft, err)
		return
	}

	// Complete
	ft.Status = FileTransferCompleted
	c.storage.UpdateFileTransferStatus(ft.ID, string(FileTransferCompleted), hash)

	// Save message about file transfer
	fileMsg := &Message{
		PeerID:     peerID,
		Content:    fmt.Sprintf("📎 Sent file: %s (%.1f MB)", ft.FileName, float64(ft.FileSize)/(1024*1024)),
		Timestamp:  time.Now(),
		IsOutgoing: true,
		IsRead:     true,
	}
	c.storage.SaveMessage(fileMsg)

	slog.Info("File transfer completed", "peerID", hexID+"...", "transferID", ft.ID, "hash", hash[:16]+"...")

	c.events <- ChatEvent{
		Type:         ChatEventFileTransferCompleted,
		PeerID:       peerID,
		FileTransfer: ft,
	}
}

// handleFileTransferMessage handles file transfer messages
func (c *Chat) handleFileTransferMessage(peerID router.PeerID, msg *FileTransferMessage) {
	hexID := hex.EncodeToString(peerID[:8])

	switch msg.Type {
	case FileTransferStart:
		slog.Info("Receiving file transfer request", "peerID", hexID+"...", "file", msg.FileName, "size", msg.FileSize)

		ft, err := c.fileTransferMgr.StartReceiving(peerID, msg)
		if err != nil {
			slog.Error("Failed to start receiving", "error", err)
			c.sendFileTransferCancel(peerID, msg.TransferID)
			return
		}

		// Save to database
		c.storage.SaveFileTransfer(ft.ID, peerID, ft.FileName, ft.FileSize, ft.FilePath, false, string(FileTransferTransferring))

		c.events <- ChatEvent{
			Type:         ChatEventFileTransferStarted,
			PeerID:       peerID,
			FileTransfer: ft,
		}

	case FileTransferChunk:
		ft, ok := c.fileTransferMgr.GetTransfer(msg.TransferID)
		if !ok {
			slog.Error("Transfer not found", "transferID", msg.TransferID)
			return
		}

		// Write chunk
		if _, err := ft.File.Write(msg.Data); err != nil {
			slog.Error("Failed to write chunk", "error", err)
			c.handleFileTransferError(ft, err)
			return
		}

		// Mark chunk as received
		ft.ChunksRecv[msg.ChunkIndex] = true

		// Update progress
		ft.UpdateProgress(len(ft.ChunksRecv))
		c.storage.UpdateFileTransferProgress(ft.ID, ft.Progress)

		// Send progress event every 10%
		if ft.Progress%10 == 0 {
			c.events <- ChatEvent{
				Type:         ChatEventFileTransferProgress,
				PeerID:       peerID,
				FileTransfer: ft,
			}
		}

		slog.Debug("Received chunk", "peerID", hexID+"...", "transferID", ft.ID, "chunk", msg.ChunkIndex, "progress", ft.Progress)

	case FileTransferEnd:
		ft, ok := c.fileTransferMgr.GetTransfer(msg.TransferID)
		if !ok {
			slog.Error("Transfer not found", "transferID", msg.TransferID)
			return
		}

		ft.File.Close()

		// Check hash
		hash, err := CalculateFileHash(ft.FilePath)
		if err != nil {
			slog.Error("Failed to calculate hash", "error", err)
			c.handleFileTransferError(ft, err)
			return
		}

		if hash != msg.SHA256Hash {
			slog.Error("Hash mismatch", "expected", msg.SHA256Hash[:16]+"...", "got", hash[:16]+"...")
			c.handleFileTransferError(ft, fmt.Errorf("hash mismatch"))
			return
		}

		// Successfully completed
		ft.Status = FileTransferCompleted
		ft.Hash = hash
		c.storage.UpdateFileTransferStatus(ft.ID, string(FileTransferCompleted), hash)

		// Save message about received file
		fileMsg := &Message{
			PeerID:     peerID,
			Content:    fmt.Sprintf("📎 Received file: %s (%.1f MB) → %s", ft.FileName, float64(ft.FileSize)/(1024*1024), ft.FilePath),
			Timestamp:  time.Now(),
			IsOutgoing: false,
			IsRead:     false,
		}
		c.storage.SaveMessage(fileMsg)

		slog.Info("File transfer completed successfully", "peerID", hexID+"...", "transferID", ft.ID, "file", ft.FileName)

		c.events <- ChatEvent{
			Type:         ChatEventFileTransferCompleted,
			PeerID:       peerID,
			FileTransfer: ft,
		}

	case FileTransferCancel:
		ft, ok := c.fileTransferMgr.GetTransfer(msg.TransferID)
		if !ok {
			return
		}

		ft.Status = FileTransferCancelled
		ft.File.Close()
		c.storage.UpdateFileTransferStatus(ft.ID, string(FileTransferCancelled), "")

		slog.Info("File transfer cancelled", "peerID", hexID+"...", "transferID", ft.ID)

		c.events <- ChatEvent{
			Type:         ChatEventFileTransferFailed,
			PeerID:       peerID,
			FileTransfer: ft,
			Error:        fmt.Errorf("transfer cancelled by peer"),
		}
	}
}

// handleFileTransferError handles file transfer error
func (c *Chat) handleFileTransferError(ft *FileTransfer, err error) {
	ft.mu.Lock()
	ft.Status = FileTransferFailed
	ft.File.Close()
	ft.mu.Unlock()

	c.storage.UpdateFileTransferStatus(ft.ID, string(FileTransferFailed), "")
	c.sendFileTransferCancel(ft.PeerID, ft.ID)

	c.events <- ChatEvent{
		Type:         ChatEventFileTransferFailed,
		PeerID:       ft.PeerID,
		FileTransfer: ft,
		Error:        err,
	}
}

// sendFileTransferCancel sends transfer cancellation message
func (c *Chat) sendFileTransferCancel(peerID router.PeerID, transferID string) {
	peer, ok := c.connector.GetPeer(peerID)
	if !ok {
		return
	}

	cancelMsg := &FileTransferMessage{
		Type:       FileTransferCancel,
		TransferID: transferID,
	}

	data, err := json.Marshal(cancelMsg)
	if err != nil {
		return
	}

	peer.Send(data)
}

// autoReconnect periodically attempts to reconnect to offline contacts
func (c *Chat) autoReconnect() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	// First attempt immediately on startup
	c.tryReconnectAll()

	for range ticker.C {
		c.tryReconnectAll()
	}
}

// tryReconnectAll attempts to connect to all offline contacts
func (c *Chat) tryReconnectAll() {
	contacts, err := c.storage.GetAllContacts()
	if err != nil {
		slog.Error("Failed to get contacts for auto-reconnect", "error", err)
		return
	}

	for _, contact := range contacts {
		// Skip blocked contacts
		if contact.IsBlocked {
			continue
		}

		// Check if contact is online
		if c.IsOnline(contact.PeerID) {
			continue
		}

		// Attempt to connect
		hexID := hex.EncodeToString(contact.PeerID[:])
		hexShort := hex.EncodeToString(contact.PeerID[:8])
		slog.Debug("Auto-reconnect attempt", "peerID", hexShort+"...", "name", contact.Name)

		if err := c.Connect(hexID); err != nil {
			slog.Debug("Auto-reconnect failed", "peerID", hexShort+"...", "error", err)
		}
	}
}

// Close closes the chat
func (c *Chat) Close() error {
	c.connector.DisconnectAll()
	return c.storage.Close()
}
