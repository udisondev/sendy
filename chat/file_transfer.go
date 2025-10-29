package chat

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/udisondev/sendy/router"
)

const (
	MaxFileSize    = 200 * 1024 * 1024 // 200 MB
	ChunkSize      = 64 * 1024          // 64 KB chunks
	FileTransferV1 = "FILE_TRANSFER_V1"
)

// FileTransferType defines file transfer message type
type FileTransferType uint8

const (
	FileTransferStart FileTransferType = iota // Start of transfer (metadata)
	FileTransferChunk                         // Data chunk
	FileTransferEnd                           // End of transfer (with hash)
	FileTransferAck                           // Acknowledgment of chunk receipt
	FileTransferCancel                        // Transfer cancellation
)

// FileTransferMessage represents a file transfer message
type FileTransferMessage struct {
	Type        FileTransferType `json:"type"`
	TransferID  string           `json:"transfer_id"`  // Unique transfer ID
	FileName    string           `json:"file_name"`    // File name
	FileSize    int64            `json:"file_size"`    // File size
	MimeType    string           `json:"mime_type"`    // MIME type
	ChunkIndex  int              `json:"chunk_index"`  // Chunk index
	TotalChunks int              `json:"total_chunks"` // Total chunks
	Data        []byte           `json:"data"`         // Chunk data
	SHA256Hash  string           `json:"sha256_hash"`  // SHA256 file hash
}

// FileTransfer represents an active file transfer
type FileTransfer struct {
	ID          string
	PeerID      router.PeerID
	FileName    string
	FileSize    int64
	FilePath    string // File path (for sending or saving)
	IsOutgoing  bool
	Status      FileTransferStatus
	Progress    int // Completion percentage
	ChunksRecv  map[int]bool
	TotalChunks int
	File        *os.File
	Hash        string
	StartedAt   time.Time
	mu          sync.Mutex
}

// FileTransferStatus defines transfer status
type FileTransferStatus string

const (
	FileTransferPending      FileTransferStatus = "pending"
	FileTransferTransferring FileTransferStatus = "transferring"
	FileTransferCompleted    FileTransferStatus = "completed"
	FileTransferFailed       FileTransferStatus = "failed"
	FileTransferCancelled    FileTransferStatus = "cancelled"
)

// FileTransferManager manages file transfers
type FileTransferManager struct {
	storage   *Storage
	dataDir   string
	transfers sync.Map // map[transferID]*FileTransfer
	mu        sync.Mutex
}

// NewFileTransferManager creates a new transfer manager
func NewFileTransferManager(storage *Storage, dataDir string) *FileTransferManager {
	filesDir := filepath.Join(dataDir, "files")
	os.MkdirAll(filesDir, 0755)

	return &FileTransferManager{
		storage: storage,
		dataDir: filesDir,
	}
}

// GenerateTransferID generates unique transfer ID
func GenerateTransferID(peerID router.PeerID, fileName string) string {
	h := sha256.New()
	h.Write(peerID[:])
	h.Write([]byte(fileName))
	h.Write([]byte(time.Now().String()))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// ValidateFileName checks file name for security
func ValidateFileName(fileName string) error {
	// Check for path traversal
	if filepath.Base(fileName) != fileName {
		return fmt.Errorf("invalid file name: path traversal detected")
	}
	if fileName == "" || fileName == "." || fileName == ".." {
		return fmt.Errorf("invalid file name")
	}
	if len(fileName) > 255 {
		return fmt.Errorf("file name too long (max 255 characters)")
	}
	return nil
}

// StartSending starts file sending
func (ftm *FileTransferManager) StartSending(peerID router.PeerID, filePath string) (*FileTransfer, error) {
	// Check that file exists and read its size
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("stat file: %w", err)
	}

	if fileInfo.Size() > MaxFileSize {
		return nil, fmt.Errorf("file too large: %d bytes (max %d)", fileInfo.Size(), MaxFileSize)
	}

	fileName := filepath.Base(filePath)
	if err := ValidateFileName(fileName); err != nil {
		return nil, err
	}

	// Open file for reading
	file, err := os.Open(filePath)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}

	transferID := GenerateTransferID(peerID, fileName)
	totalChunks := int((fileInfo.Size() + ChunkSize - 1) / ChunkSize)

	ft := &FileTransfer{
		ID:          transferID,
		PeerID:      peerID,
		FileName:    fileName,
		FileSize:    fileInfo.Size(),
		FilePath:    filePath,
		IsOutgoing:  true,
		Status:      FileTransferPending,
		Progress:    0,
		TotalChunks: totalChunks,
		File:        file,
		StartedAt:   time.Now(),
	}

	ftm.transfers.Store(transferID, ft)
	return ft, nil
}

// StartReceiving starts file receiving
func (ftm *FileTransferManager) StartReceiving(peerID router.PeerID, msg *FileTransferMessage) (*FileTransfer, error) {
	if err := ValidateFileName(msg.FileName); err != nil {
		return nil, err
	}

	if msg.FileSize > MaxFileSize {
		return nil, fmt.Errorf("file too large: %d bytes (max %d)", msg.FileSize, MaxFileSize)
	}

	// Create file for writing
	filePath := filepath.Join(ftm.dataDir, msg.TransferID+"_"+msg.FileName)
	file, err := os.Create(filePath)
	if err != nil {
		return nil, fmt.Errorf("create file: %w", err)
	}

	ft := &FileTransfer{
		ID:          msg.TransferID,
		PeerID:      peerID,
		FileName:    msg.FileName,
		FileSize:    msg.FileSize,
		FilePath:    filePath,
		IsOutgoing:  false,
		Status:      FileTransferTransferring,
		Progress:    0,
		ChunksRecv:  make(map[int]bool),
		TotalChunks: msg.TotalChunks,
		File:        file,
		StartedAt:   time.Now(),
	}

	ftm.transfers.Store(msg.TransferID, ft)
	return ft, nil
}

// GetTransfer returns transfer by ID
func (ftm *FileTransferManager) GetTransfer(transferID string) (*FileTransfer, bool) {
	val, ok := ftm.transfers.Load(transferID)
	if !ok {
		return nil, false
	}
	return val.(*FileTransfer), true
}

// EncodeFileMessage encodes file transfer message
func EncodeFileMessage(msg *FileTransferMessage) ([]byte, error) {
	return json.Marshal(msg)
}

// DecodeFileMessage decodes file transfer message
func DecodeFileMessage(data []byte) (*FileTransferMessage, error) {
	var msg FileTransferMessage
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

// UpdateProgress updates transfer progress
func (ft *FileTransfer) UpdateProgress(chunksCompleted int) {
	ft.mu.Lock()
	defer ft.mu.Unlock()

	if ft.TotalChunks > 0 {
		ft.Progress = (chunksCompleted * 100) / ft.TotalChunks
	}
}

// Close closes transfer file
func (ft *FileTransfer) Close() error {
	ft.mu.Lock()
	defer ft.mu.Unlock()

	if ft.File != nil {
		return ft.File.Close()
	}
	return nil
}

// CalculateFileHash calculates SHA256 hash of file
func CalculateFileHash(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}

	return hex.EncodeToString(hash.Sum(nil)), nil
}
