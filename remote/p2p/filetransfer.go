package p2p

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"os"
	"path/filepath"
)

// FileTransferProtocol handles file transfer over WebRTC data channels
type FileTransferProtocol struct {
	conn   *Conn
	logger *slog.Logger
}

// FileMetadata contains information about the file being transferred
type FileMetadata struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	Hash     string `json:"hash"` // SHA256 hash of the file
}

type TransferMessage struct {
	Type     string        `json:"type"` // "metadata" or "data"
	Metadata *FileMetadata `json:"metadata,omitempty"`
	Data     []byte        `json:"data,omitempty"`
	Offset   int64         `json:"offset,omitempty"`
}

const (
	MessageTypeMetadata = "metadata"
	MessageTypeData     = "data"
	MessageTypeComplete = "complete"
	MessageTypeError    = "error"

	ChunkSize = 16384 // 16KB chunks
)

func NewFileTransferProtocol(conn *Conn, logger *slog.Logger) *FileTransferProtocol {
	if logger == nil {
		logger = slog.Default()
	}

	return &FileTransferProtocol{
		conn:   conn,
		logger: logger,
	}
}

// SendFile sends a file over the WebRTC connection
func (ftp *FileTransferProtocol) SendFile(filePath string) error {
	// Open the file
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	// Get file info
	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat file: %w", err)
	}

	filename := filepath.Base(filePath)

	// Calculate SHA256 hash
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("failed to calculate file hash: %w", err)
	}
	hashString := hex.EncodeToString(hash.Sum(nil))

	// Reset file position to beginning
	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("failed to reset file position: %w", err)
	}

	metadata := &FileMetadata{
		Filename: filename,
		Size:     fileInfo.Size(),
		Hash:     hashString,
	}

	ftp.logger.Info("Starting file transfer", "filename", filename, "size", fileInfo.Size(), "hash", hashString[:16]+"...")

	// Send metadata
	if err := ftp.sendMetadata(metadata); err != nil {
		return fmt.Errorf("failed to send metadata: %w", err)
	}

	// Send file data in chunks
	buffer := make([]byte, ChunkSize)
	var totalSent int64

	for {
		n, err := file.Read(buffer)
		if err != nil && err != io.EOF {
			return fmt.Errorf("failed to read file: %w", err)
		}

		if n == 0 {
			break // End of file
		}

		// Send data chunk
		dataMsg := &TransferMessage{
			Type:   MessageTypeData,
			Data:   buffer[:n],
			Offset: totalSent,
		}

		if err := ftp.sendMessage(dataMsg); err != nil {
			return fmt.Errorf("failed to send data chunk: %w", err)
		}

		totalSent += int64(n)

		// Progress logging
		if totalSent%(1024*1024) == 0 || totalSent == fileInfo.Size() { // Every MB or at completion
			progress := float64(totalSent) / float64(fileInfo.Size()) * 100
			ftp.logger.Info("Transfer progress", "sent", totalSent, "total", fileInfo.Size(), "progress", fmt.Sprintf("%.1f%%", progress))
		}
	}

	// Send completion message
	completeMsg := &TransferMessage{
		Type: MessageTypeComplete,
	}
	if err := ftp.sendMessage(completeMsg); err != nil {
		return fmt.Errorf("failed to send completion message: %w", err)
	}

	ftp.logger.Info("File transfer completed", "filename", filename, "total_sent", totalSent, "hash", hashString[:16]+"...")
	return nil
}

// ReceiveFile receives a file over the WebRTC connection and saves it to outputPath
func (ftp *FileTransferProtocol) ReceiveFile(outputPath string) error {
	var metadata *FileMetadata
	var outputFile *os.File
	var receivedSize int64
	var hasher hash.Hash

	defer func() {
		if outputFile != nil {
			outputFile.Close()
		}
	}()

	for {
		buffer := make([]byte, ChunkSize*2) // Buffer larger than chunk size
		n, err := ftp.conn.Read(buffer)
		if err != nil {
			return fmt.Errorf("failed to read from connection: %w", err)
		}

		var msg TransferMessage
		if err := json.Unmarshal(buffer[:n], &msg); err != nil {
			return fmt.Errorf("failed to unmarshal message: %w", err)
		}

		switch msg.Type {
		case MessageTypeMetadata:
			if msg.Metadata == nil {
				return fmt.Errorf("received metadata message but metadata is nil")
			}
			metadata = msg.Metadata
			ftp.logger.Info("Received file metadata", "filename", metadata.Filename, "size", metadata.Size, "hash", metadata.Hash[:16]+"...")

			// Initialize hash calculator
			hasher = sha256.New()

			// Create output file
			// Create directory if it doesn't exist
			dir := filepath.Dir(outputPath)
			if dir != "." {
				if err := os.MkdirAll(dir, 0755); err != nil {
					return fmt.Errorf("failed to create output directory: %w", err)
				}
			}

			outputFile, err = os.Create(outputPath)
			if err != nil {
				return fmt.Errorf("failed to create output file: %w", err)
			}

		case MessageTypeData:
			if outputFile == nil {
				return fmt.Errorf("received data before metadata")
			}

			if _, err := outputFile.Write(msg.Data); err != nil {
				return fmt.Errorf("failed to write to output file: %w", err)
			}

			// Update hash
			if hasher != nil {
				hasher.Write(msg.Data)
			}

			receivedSize += int64(len(msg.Data))

			// Progress logging
			if metadata != nil && (receivedSize%(1024*1024) == 0 || receivedSize == metadata.Size) {
				progress := float64(receivedSize) / float64(metadata.Size) * 100
				ftp.logger.Info("Receive progress", "received", receivedSize, "total", metadata.Size, "progress", fmt.Sprintf("%.1f%%", progress))
			}

		case MessageTypeComplete:
			if outputFile == nil {
				return fmt.Errorf("received completion before metadata")
			}

			outputFile.Close()
			outputFile = nil

			if metadata != nil {
				ftp.logger.Info("File transfer completed", "filename", metadata.Filename, "received", receivedSize, "expected", metadata.Size)
				if receivedSize != metadata.Size {
					return fmt.Errorf("received size (%d) doesn't match expected size (%d)", receivedSize, metadata.Size)
				}

				// Verify hash
				if hasher != nil {
					receivedHash := hex.EncodeToString(hasher.Sum(nil))
					if receivedHash != metadata.Hash {
						return fmt.Errorf("file hash verification failed: expected %s, got %s", metadata.Hash, receivedHash)
					}
					ftp.logger.Info("File hash verified", "hash", receivedHash[:16]+"...")
				}
			}

			return nil

		case MessageTypeError:
			return fmt.Errorf("received error from sender: %s", string(msg.Data))

		default:
			ftp.logger.Warn("Received unknown message type", "type", msg.Type)
		}
	}
}

func (ftp *FileTransferProtocol) sendMetadata(metadata *FileMetadata) error {
	msg := &TransferMessage{
		Type:     MessageTypeMetadata,
		Metadata: metadata,
	}
	return ftp.sendMessage(msg)
}

func (ftp *FileTransferProtocol) sendMessage(msg *TransferMessage) error {
	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}

	_, err = ftp.conn.Write(data)
	if err != nil {
		return fmt.Errorf("failed to write message: %w", err)
	}

	return nil
}

// SendError sends an error message to the other side
func (ftp *FileTransferProtocol) SendError(errorMsg string) error {
	msg := &TransferMessage{
		Type: MessageTypeError,
		Data: []byte(errorMsg),
	}
	return ftp.sendMessage(msg)
}
