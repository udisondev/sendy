package chat

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	_ "github.com/mattn/go-sqlite3"
	"github.com/udisondev/sendy/router"
)

// Security limits
const (
	MaxMessageSize  = 10 * 1024 * 1024 // 10 MB - maximum message size
	MaxContactName  = 256              // Maximum contact name length
	MaxContactCount = 10000            // Maximum number of contacts
)

// Storage manages message and contact storage
type Storage struct {
	db *sql.DB
}

// Contact represents a contact in address book
type Contact struct {
	PeerID              router.PeerID
	Name                string
	AddedAt             time.Time
	LastSeen            time.Time
	IsBlocked           bool
	NotificationsBlocked bool // Block notifications from this contact
}

// Message represents a message in chat
type Message struct {
	ID        int64
	PeerID    router.PeerID
	Content   string
	Timestamp time.Time
	IsOutgoing bool // true if we sent, false if received
	IsRead    bool
}

// SearchResult represents a search result with contact info
type SearchResult struct {
	Message
	ContactName string
}

// NewStorage creates a new storage
func NewStorage(dbPath string) (*Storage, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}

	s := &Storage{db: db}
	if err := s.init(); err != nil {
		db.Close()
		return nil, err
	}

	return s, nil
}

// init initializes database schema
func (s *Storage) init() error {
	schema := `
	CREATE TABLE IF NOT EXISTS contacts (
		peer_id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		added_at INTEGER NOT NULL,
		last_seen INTEGER NOT NULL,
		is_blocked INTEGER NOT NULL DEFAULT 0,
		notifications_blocked INTEGER NOT NULL DEFAULT 0
	);

	CREATE TABLE IF NOT EXISTS messages (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		peer_id TEXT NOT NULL,
		content TEXT NOT NULL,
		timestamp INTEGER NOT NULL,
		is_outgoing INTEGER NOT NULL,
		is_read INTEGER NOT NULL DEFAULT 0,
		FOREIGN KEY(peer_id) REFERENCES contacts(peer_id)
	);

	CREATE INDEX IF NOT EXISTS idx_messages_peer_timestamp
	ON messages(peer_id, timestamp DESC);

	CREATE INDEX IF NOT EXISTS idx_messages_unread
	ON messages(peer_id, is_read) WHERE is_read = 0;

	CREATE TABLE IF NOT EXISTS file_transfers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		transfer_id TEXT UNIQUE NOT NULL,
		peer_id TEXT NOT NULL,
		file_name TEXT NOT NULL,
		file_size INTEGER NOT NULL,
		file_path TEXT,
		is_outgoing INTEGER NOT NULL,
		status TEXT NOT NULL,
		progress INTEGER DEFAULT 0,
		sha256_hash TEXT,
		started_at INTEGER NOT NULL,
		completed_at INTEGER,
		FOREIGN KEY(peer_id) REFERENCES contacts(peer_id)
	);

	CREATE INDEX IF NOT EXISTS idx_file_transfers_peer
	ON file_transfers(peer_id, started_at DESC);

	CREATE INDEX IF NOT EXISTS idx_file_transfers_status
	ON file_transfers(status, started_at DESC);
	`

	_, err := s.db.Exec(schema)
	if err != nil {
		return err
	}

	// Migration: add notifications_blocked for existing databases
	_, err = s.db.Exec(`
		ALTER TABLE contacts ADD COLUMN notifications_blocked INTEGER NOT NULL DEFAULT 0;
	`)
	if err != nil && !strings.Contains(err.Error(), "duplicate column name") {
		return err
	}

	return nil
}

// Close closes database connection
func (s *Storage) Close() error {
	return s.db.Close()
}

// AddContact adds a new contact
func (s *Storage) AddContact(peerID router.PeerID, name string) error {
	// SECURITY: Validate contact name
	if len(name) == 0 {
		return fmt.Errorf("contact name cannot be empty")
	}
	if len(name) > MaxContactName {
		return fmt.Errorf("contact name too long: %d bytes (max %d)", len(name), MaxContactName)
	}

	// SECURITY: Check contact limit
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM contacts`).Scan(&count); err != nil {
		return fmt.Errorf("check contact count: %w", err)
	}
	if count >= MaxContactCount {
		return fmt.Errorf("contact limit reached: %d (max %d)", count, MaxContactCount)
	}

	hexID := hex.EncodeToString(peerID[:])
	now := time.Now().Unix()

	_, err := s.db.Exec(`
		INSERT INTO contacts (peer_id, name, added_at, last_seen, is_blocked)
		VALUES (?, ?, ?, ?, 0)
		ON CONFLICT(peer_id) DO UPDATE SET name = excluded.name
	`, hexID, name, now, now)

	return err
}

// UpdateContactName updates contact name
func (s *Storage) UpdateContactName(peerID router.PeerID, name string) error {
	// SECURITY: Validate contact name
	if len(name) == 0 {
		return fmt.Errorf("contact name cannot be empty")
	}
	if len(name) > MaxContactName {
		return fmt.Errorf("contact name too long: %d bytes (max %d)", len(name), MaxContactName)
	}

	hexID := hex.EncodeToString(peerID[:])
	_, err := s.db.Exec(`UPDATE contacts SET name = ? WHERE peer_id = ?`, name, hexID)
	return err
}

// UpdateLastSeen updates contact's last activity time
func (s *Storage) UpdateLastSeen(peerID router.PeerID) error {
	hexID := hex.EncodeToString(peerID[:])
	now := time.Now().Unix()
	_, err := s.db.Exec(`UPDATE contacts SET last_seen = ? WHERE peer_id = ?`, now, hexID)
	return err
}

// SetBlocked sets contact blocked status
func (s *Storage) SetBlocked(peerID router.PeerID, blocked bool) error {
	hexID := hex.EncodeToString(peerID[:])
	_, err := s.db.Exec(`UPDATE contacts SET is_blocked = ? WHERE peer_id = ?`, blocked, hexID)
	return err
}

// SetNotificationsBlocked sets notification blocking for contact
func (s *Storage) SetNotificationsBlocked(peerID router.PeerID, blocked bool) error {
	hexID := hex.EncodeToString(peerID[:])
	_, err := s.db.Exec(`UPDATE contacts SET notifications_blocked = ? WHERE peer_id = ?`, blocked, hexID)
	return err
}

// DeleteContact deletes contact and all conversation history
func (s *Storage) DeleteContact(peerID router.PeerID) error {
	hexID := hex.EncodeToString(peerID[:])

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Delete messages
	if _, err := tx.Exec(`DELETE FROM messages WHERE peer_id = ?`, hexID); err != nil {
		return err
	}

	// Delete contact
	if _, err := tx.Exec(`DELETE FROM contacts WHERE peer_id = ?`, hexID); err != nil {
		return err
	}

	return tx.Commit()
}

// GetContact returns contact by ID
func (s *Storage) GetContact(peerID router.PeerID) (*Contact, error) {
	hexID := hex.EncodeToString(peerID[:])

	var contact Contact
	var hexStr string
	var addedAt, lastSeen int64
	var isBlocked, notificationsBlocked int

	err := s.db.QueryRow(`
		SELECT peer_id, name, added_at, last_seen, is_blocked, notifications_blocked
		FROM contacts WHERE peer_id = ?
	`, hexID).Scan(&hexStr, &contact.Name, &addedAt, &lastSeen, &isBlocked, &notificationsBlocked)

	if err != nil {
		return nil, err
	}

	// SECURITY: Check hex decoding error
	peerIDBytes, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, fmt.Errorf("invalid peer_id in database: %w", err)
	}
	if len(peerIDBytes) != router.PeerIDSize {
		return nil, fmt.Errorf("invalid peer_id size in database: got %d, expected %d", len(peerIDBytes), router.PeerIDSize)
	}

	copy(contact.PeerID[:], peerIDBytes)
	contact.AddedAt = time.Unix(addedAt, 0)
	contact.LastSeen = time.Unix(lastSeen, 0)
	contact.IsBlocked = isBlocked != 0
	contact.NotificationsBlocked = notificationsBlocked != 0

	return &contact, nil
}

// GetAllContacts returns all contacts
func (s *Storage) GetAllContacts() ([]*Contact, error) {
	rows, err := s.db.Query(`
		SELECT peer_id, name, added_at, last_seen, is_blocked, notifications_blocked
		FROM contacts
		ORDER BY last_seen DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var contacts []*Contact
	for rows.Next() {
		var contact Contact
		var hexStr string
		var addedAt, lastSeen int64
		var isBlocked, notificationsBlocked int

		if err := rows.Scan(&hexStr, &contact.Name, &addedAt, &lastSeen, &isBlocked, &notificationsBlocked); err != nil {
			return nil, err
		}

		// SECURITY: Check hex decoding error
		peerIDBytes, err := hex.DecodeString(hexStr)
		if err != nil {
			return nil, fmt.Errorf("invalid peer_id in database: %w", err)
		}
		if len(peerIDBytes) != router.PeerIDSize {
			return nil, fmt.Errorf("invalid peer_id size in database: got %d, expected %d", len(peerIDBytes), router.PeerIDSize)
		}

		copy(contact.PeerID[:], peerIDBytes)
		contact.AddedAt = time.Unix(addedAt, 0)
		contact.LastSeen = time.Unix(lastSeen, 0)
		contact.IsBlocked = isBlocked != 0
		contact.NotificationsBlocked = notificationsBlocked != 0

		contacts = append(contacts, &contact)
	}

	return contacts, rows.Err()
}

// SaveMessage saves a message
func (s *Storage) SaveMessage(msg *Message) error {
	// SECURITY: Validate message size
	if len(msg.Content) == 0 {
		return fmt.Errorf("message content cannot be empty")
	}
	if len(msg.Content) > MaxMessageSize {
		return fmt.Errorf("message too large: %d bytes (max %d)", len(msg.Content), MaxMessageSize)
	}

	hexID := hex.EncodeToString(msg.PeerID[:])
	timestamp := msg.Timestamp.Unix()

	result, err := s.db.Exec(`
		INSERT INTO messages (peer_id, content, timestamp, is_outgoing, is_read)
		VALUES (?, ?, ?, ?, ?)
	`, hexID, msg.Content, timestamp, msg.IsOutgoing, msg.IsRead)

	if err != nil {
		return err
	}

	msg.ID, _ = result.LastInsertId()
	return nil
}

// GetMessages returns messages with a contact
func (s *Storage) GetMessages(peerID router.PeerID, limit int) ([]*Message, error) {
	hexID := hex.EncodeToString(peerID[:])

	rows, err := s.db.Query(`
		SELECT id, peer_id, content, timestamp, is_outgoing, is_read
		FROM messages
		WHERE peer_id = ?
		ORDER BY timestamp DESC
		LIMIT ?
	`, hexID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []*Message
	for rows.Next() {
		var msg Message
		var hexStr string
		var timestamp int64
		var isOutgoing, isRead int

		if err := rows.Scan(&msg.ID, &hexStr, &msg.Content, &timestamp, &isOutgoing, &isRead); err != nil {
			return nil, err
		}

		// SECURITY: Check hex decoding error
		peerIDBytes, err := hex.DecodeString(hexStr)
		if err != nil {
			return nil, fmt.Errorf("invalid peer_id in database: %w", err)
		}
		if len(peerIDBytes) != router.PeerIDSize {
			return nil, fmt.Errorf("invalid peer_id size in database: got %d, expected %d", len(peerIDBytes), router.PeerIDSize)
		}

		copy(msg.PeerID[:], peerIDBytes)
		msg.Timestamp = time.Unix(timestamp, 0)
		msg.IsOutgoing = isOutgoing != 0
		msg.IsRead = isRead != 0

		messages = append(messages, &msg)
	}

	// Reverse so old messages are first
	for i := 0; i < len(messages)/2; i++ {
		j := len(messages) - 1 - i
		messages[i], messages[j] = messages[j], messages[i]
	}

	return messages, rows.Err()
}

// MarkAsRead marks all messages from contact as read
func (s *Storage) MarkAsRead(peerID router.PeerID) error {
	hexID := hex.EncodeToString(peerID[:])
	_, err := s.db.Exec(`
		UPDATE messages SET is_read = 1
		WHERE peer_id = ? AND is_outgoing = 0 AND is_read = 0
	`, hexID)
	return err
}

// GetUnreadCount returns the number of unread messages from contact
func (s *Storage) GetUnreadCount(peerID router.PeerID) (int, error) {
	hexID := hex.EncodeToString(peerID[:])

	var count int
	err := s.db.QueryRow(`
		SELECT COUNT(*) FROM messages
		WHERE peer_id = ? AND is_outgoing = 0 AND is_read = 0
	`, hexID).Scan(&count)

	return count, err
}

// SaveFileTransfer saves file transfer information
func (s *Storage) SaveFileTransfer(transferID string, peerID router.PeerID, fileName string, fileSize int64, filePath string, isOutgoing bool, status string) error {
	hexID := hex.EncodeToString(peerID[:])
	now := time.Now().Unix()

	_, err := s.db.Exec(`
		INSERT INTO file_transfers (transfer_id, peer_id, file_name, file_size, file_path, is_outgoing, status, progress, started_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?)
		ON CONFLICT(transfer_id) DO UPDATE SET
			status = excluded.status,
			file_path = excluded.file_path
	`, transferID, hexID, fileName, fileSize, filePath, isOutgoing, status, now)

	return err
}

// UpdateFileTransferProgress updates transfer progress
func (s *Storage) UpdateFileTransferProgress(transferID string, progress int) error {
	_, err := s.db.Exec(`
		UPDATE file_transfers SET progress = ?
		WHERE transfer_id = ?
	`, progress, transferID)
	return err
}

// UpdateFileTransferStatus updates transfer status
func (s *Storage) UpdateFileTransferStatus(transferID string, status string, hash string) error {
	now := time.Now().Unix()
	_, err := s.db.Exec(`
		UPDATE file_transfers
		SET status = ?, sha256_hash = ?, completed_at = ?
		WHERE transfer_id = ?
	`, status, hash, now, transferID)
	return err
}

// GetFileTransfer returns transfer information by ID
func (s *Storage) GetFileTransfer(transferID string) (peerID router.PeerID, fileName string, fileSize int64, filePath string, isOutgoing bool, status string, progress int, err error) {
	var hexID string
	var isOut int

	err = s.db.QueryRow(`
		SELECT peer_id, file_name, file_size, file_path, is_outgoing, status, progress
		FROM file_transfers
		WHERE transfer_id = ?
	`, transferID).Scan(&hexID, &fileName, &fileSize, &filePath, &isOut, &status, &progress)

	if err != nil {
		return
	}

	peerIDBytes, err := hex.DecodeString(hexID)
	if err != nil {
		return
	}
	copy(peerID[:], peerIDBytes)
	isOutgoing = isOut != 0

	return
}

// GetFileTransfers returns list of transfers for contact
func (s *Storage) GetFileTransfers(peerID router.PeerID, limit int) ([]struct {
	TransferID  string
	FileName    string
	FileSize    int64
	IsOutgoing  bool
	Status      string
	Progress    int
	StartedAt   time.Time
	CompletedAt *time.Time
}, error) {
	hexID := hex.EncodeToString(peerID[:])

	rows, err := s.db.Query(`
		SELECT transfer_id, file_name, file_size, is_outgoing, status, progress, started_at, completed_at
		FROM file_transfers
		WHERE peer_id = ?
		ORDER BY started_at DESC
		LIMIT ?
	`, hexID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var transfers []struct {
		TransferID  string
		FileName    string
		FileSize    int64
		IsOutgoing  bool
		Status      string
		Progress    int
		StartedAt   time.Time
		CompletedAt *time.Time
	}

	for rows.Next() {
		var t struct {
			TransferID  string
			FileName    string
			FileSize    int64
			IsOutgoing  bool
			Status      string
			Progress    int
			StartedAt   time.Time
			CompletedAt *time.Time
		}
		var isOut int
		var startedAt int64
		var completedAt sql.NullInt64

		if err := rows.Scan(&t.TransferID, &t.FileName, &t.FileSize, &isOut, &t.Status, &t.Progress, &startedAt, &completedAt); err != nil {
			return nil, err
		}

		t.IsOutgoing = isOut != 0
		t.StartedAt = time.Unix(startedAt, 0)
		if completedAt.Valid {
			ct := time.Unix(completedAt.Int64, 0)
			t.CompletedAt = &ct
		}

		transfers = append(transfers, t)
	}

	return transfers, rows.Err()
}

// SearchMessages searches for messages containing the query string
// Returns results from all contacts, sorted by timestamp (newest first)
func (s *Storage) SearchMessages(query string, limit int) ([]*SearchResult, error) {
	if query == "" {
		return nil, nil
	}

	// Use LIKE for case-insensitive search
	// Add % wildcards for substring matching
	searchPattern := "%" + query + "%"

	rows, err := s.db.Query(`
		SELECT
			m.id, m.peer_id, m.content, m.timestamp, m.is_outgoing, m.is_read,
			c.name
		FROM messages m
		JOIN contacts c ON m.peer_id = c.peer_id
		WHERE m.content LIKE ? COLLATE NOCASE
		ORDER BY m.timestamp DESC
		LIMIT ?
	`, searchPattern, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []*SearchResult
	for rows.Next() {
		var result SearchResult
		var hexStr string
		var timestamp int64
		var isOutgoing, isRead int

		if err := rows.Scan(
			&result.ID, &hexStr, &result.Content,
			&timestamp, &isOutgoing, &isRead,
			&result.ContactName,
		); err != nil {
			return nil, err
		}

		// SECURITY: Validate hex decoding
		peerIDBytes, err := hex.DecodeString(hexStr)
		if err != nil {
			return nil, fmt.Errorf("invalid peer_id in database: %w", err)
		}
		if len(peerIDBytes) != router.PeerIDSize {
			return nil, fmt.Errorf("invalid peer_id size in database: got %d, expected %d", len(peerIDBytes), router.PeerIDSize)
		}

		copy(result.PeerID[:], peerIDBytes)
		result.Timestamp = time.Unix(timestamp, 0)
		result.IsOutgoing = isOutgoing != 0
		result.IsRead = isRead != 0

		results = append(results, &result)
	}

	return results, rows.Err()
}
