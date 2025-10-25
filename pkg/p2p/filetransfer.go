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

type FileTransferProtocol struct {
	conn   *Conn
	logger *slog.Logger
}

type FileMetadata struct {
	Filename string `json:"filename"`
	Size     int64  `json:"size"`
	Hash     string `json:"hash"`
}

type TransferMessage struct {
	Type     string        `json:"type"`
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

func (ftp *FileTransferProtocol) SendFile(filePath string) error {
	file, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	fileInfo, err := file.Stat()
	if err != nil {
		return fmt.Errorf("failed to stat file: %w", err)
	}

	filename := filepath.Base(filePath)

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return fmt.Errorf("failed to calculate file hash: %w", err)
	}
	hashString := hex.EncodeToString(hash.Sum(nil))

	if _, err := file.Seek(0, 0); err != nil {
		return fmt.Errorf("failed to reset file position: %w", err)
	}

	metadata := &FileMetadata{
		Filename: filename,
		Size:     fileInfo.Size(),
		Hash:     hashString,
	}

	ftp.logger.Info("Starting file transfer", "filename", filename, "size", fileInfo.Size(), "hash", hashString[:16]+"...")

	if err := ftp.sendMetadata(metadata); err != nil {
		return fmt.Errorf("failed to send metadata: %w", err)
	}

	buffer := make([]byte, ChunkSize)
	var totalSent int64

	for {
		n, err := file.Read(buffer)
		if err != nil && err != io.EOF {
			return fmt.Errorf("failed to read file: %w", err)
		}

		if n == 0 {
			break
		}

		dataMsg := &TransferMessage{
			Type:   MessageTypeData,
			Data:   buffer[:n],
			Offset: totalSent,
		}

		if err := ftp.sendMessage(dataMsg); err != nil {
			return fmt.Errorf("failed to send data chunk: %w", err)
		}

		totalSent += int64(n)

		if totalSent%(1024*1024) == 0 || totalSent == fileInfo.Size() {
			progress := float64(totalSent) / float64(fileInfo.Size()) * 100
			ftp.logger.Info("Transfer progress", "sent", totalSent, "total", fileInfo.Size(), "progress", fmt.Sprintf("%.1f%%", progress))
		}
	}

	completeMsg := &TransferMessage{
		Type: MessageTypeComplete,
	}
	if err := ftp.sendMessage(completeMsg); err != nil {
		return fmt.Errorf("failed to send completion message: %w", err)
	}

	ftp.logger.Info("File transfer completed", "filename", filename, "total_sent", totalSent, "hash", hashString[:16]+"...")
	return nil
}

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
		buffer := make([]byte, ChunkSize*2)
		n, err := ftp.conn.Read(buffer)
		if err != nil {
			if err == io.EOF {
				if metadata != nil && receivedSize == metadata.Size {
					if hasher != nil {
						receivedHash := hex.EncodeToString(hasher.Sum(nil))
						if receivedHash != metadata.Hash {
							return fmt.Errorf("file hash verification failed: expected %s, got %s", metadata.Hash, receivedHash)
						}
						ftp.logger.Info("File hash verified", "hash", receivedHash[:16]+"...")
					}
					ftp.logger.Info("Connection closed after receiving complete file", "received", receivedSize)
					return nil
				}
				if metadata != nil {
					return fmt.Errorf("connection closed unexpectedly: received %d of %d bytes", receivedSize, metadata.Size)
				}
				return fmt.Errorf("connection closed before receiving metadata")
			}
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

			hasher = sha256.New()

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

			if hasher != nil {
				hasher.Write(msg.Data)
			}

			receivedSize += int64(len(msg.Data))

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

func (ftp *FileTransferProtocol) SendError(errorMsg string) error {
	msg := &TransferMessage{
		Type: MessageTypeError,
		Data: []byte(errorMsg),
	}
	return ftp.sendMessage(msg)
}
