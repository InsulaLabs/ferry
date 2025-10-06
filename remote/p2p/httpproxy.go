package p2p

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
)

// HTTPProxyProtocol handles HTTP proxying over WebRTC data channels
type HTTPProxyProtocol struct {
	conn   *Conn
	logger *slog.Logger
}

// HTTPMessage represents an HTTP request or response
type HTTPMessage struct {
	Data []byte `json:"data"`
}

func NewHTTPProxyProtocol(conn *Conn, logger *slog.Logger) *HTTPProxyProtocol {
	if logger == nil {
		logger = slog.Default()
	}

	return &HTTPProxyProtocol{
		conn:   conn,
		logger: logger,
	}
}

// Serve starts the HTTP proxy server (receiver side)
func (hpp *HTTPProxyProtocol) Serve(tcpAddr string) error {
	hpp.logger.Info("Starting HTTP proxy server", "tcp_addr", tcpAddr)

	buffer := make([]byte, 8192)

	for {
		// Read HTTP request from WebRTC
		requestData, err := hpp.readMessage(buffer)
		if err != nil {
			if err == io.EOF {
				hpp.logger.Info("WebRTC connection closed")
				return nil
			}
			return fmt.Errorf("failed to read HTTP request from WebRTC: %w", err)
		}

		hpp.logger.Info("Received HTTP request from WebRTC", "bytes", len(requestData))

		// Connect to TCP server (web server)
		tcpConn, err := net.Dial("tcp", tcpAddr)
		if err != nil {
			hpp.logger.Error("Failed to connect to web server", "addr", tcpAddr, "error", err)
			continue
		}

		// Send HTTP request to web server
		if _, err := tcpConn.Write(requestData); err != nil {
			hpp.logger.Error("Failed to send request to web server", "error", err)
			tcpConn.Close()
			continue
		}

		// Read HTTP response from web server
		responseData, err := hpp.readHTTPResponse(tcpConn)
		if err != nil {
			hpp.logger.Error("Failed to read response from web server", "error", err)
			tcpConn.Close()
			continue
		}

		tcpConn.Close()

		hpp.logger.Info("Received HTTP response from web server", "bytes", len(responseData))

		// Send HTTP response back over WebRTC
		if err := hpp.sendMessage(responseData); err != nil {
			return fmt.Errorf("failed to send response over WebRTC: %w", err)
		}

		hpp.logger.Info("HTTP request-response cycle completed")
	}
}

// Connect starts the HTTP proxy client (sender side)
func (hpp *HTTPProxyProtocol) Connect(tcpAddr string) error {
	hpp.logger.Info("Starting HTTP proxy client", "tcp_addr", tcpAddr)

	// Start TCP listener
	listener, err := net.Listen("tcp", tcpAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", tcpAddr, err)
	}
	defer listener.Close()

	hpp.logger.Info("TCP listener started", "addr", tcpAddr)

	for {
		// Accept TCP connection from client
		tcpConn, err := listener.Accept()
		if err != nil {
			hpp.logger.Error("Failed to accept TCP connection", "error", err)
			continue
		}

		hpp.logger.Info("Accepted client connection", "remote_addr", tcpConn.RemoteAddr())

		// Handle this HTTP request-response cycle
		hpp.handleClientRequest(tcpConn)

		hpp.logger.Info("Client request completed, waiting for next connection")
	}
}

func (hpp *HTTPProxyProtocol) handleClientRequest(tcpConn net.Conn) {
	defer tcpConn.Close()

	// Read HTTP request from client
	requestData, err := hpp.readHTTPRequest(tcpConn)
	if err != nil {
		hpp.logger.Error("Failed to read HTTP request from client", "error", err)
		return
	}

	hpp.logger.Info("Received HTTP request from client", "bytes", len(requestData))

	// Send HTTP request over WebRTC to receiver
	if err := hpp.sendMessage(requestData); err != nil {
		hpp.logger.Error("Failed to send request over WebRTC", "error", err)
		return
	}

	// Read HTTP response from WebRTC
	buffer := make([]byte, 8192)
	responseData, err := hpp.readMessage(buffer)
	if err != nil {
		hpp.logger.Error("Failed to read response from WebRTC", "error", err)
		return
	}

	hpp.logger.Info("Received HTTP response from WebRTC", "bytes", len(responseData))

	// Send HTTP response back to client
	if _, err := tcpConn.Write(responseData); err != nil {
		hpp.logger.Error("Failed to send response to client", "error", err)
		return
	}

	hpp.logger.Info("HTTP request-response cycle completed for client")
}

// readHTTPRequest reads a complete HTTP request from the connection
func (hpp *HTTPProxyProtocol) readHTTPRequest(conn net.Conn) ([]byte, error) {
	reader := bufio.NewReader(conn)
	var requestData []byte

	// Read headers until we find the end of headers (\r\n\r\n)
	headersEnded := false
	for !headersEnded {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("failed to read request line: %w", err)
		}
		requestData = append(requestData, []byte(line)...)

		// Check for end of headers
		if line == "\r\n" {
			headersEnded = true
		}
	}

	// Parse Content-Length if present
	contentLength := -1
	headersStr := string(requestData)
	for _, line := range strings.Split(headersStr, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				if cl, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
					contentLength = cl
				}
			}
			break
		}
	}

	// Read body if Content-Length is specified
	if contentLength > 0 {
		body := make([]byte, contentLength)
		_, err := io.ReadFull(reader, body)
		if err != nil {
			return nil, fmt.Errorf("failed to read request body: %w", err)
		}
		requestData = append(requestData, body...)
	}

	return requestData, nil
}

// readHTTPResponse reads a complete HTTP response from the connection
func (hpp *HTTPProxyProtocol) readHTTPResponse(conn net.Conn) ([]byte, error) {
	reader := bufio.NewReader(conn)
	var responseData []byte

	// Read status line
	statusLine, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("failed to read status line: %w", err)
	}
	responseData = append(responseData, []byte(statusLine)...)

	// Read headers until we find the end of headers (\r\n\r\n)
	headersEnded := false
	for !headersEnded {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("failed to read response header: %w", err)
		}
		responseData = append(responseData, []byte(line)...)

		// Check for end of headers
		if line == "\r\n" {
			headersEnded = true
		}
	}

	// Parse Content-Length if present
	contentLength := -1
	headersStr := string(responseData)
	for _, line := range strings.Split(headersStr, "\r\n") {
		if strings.HasPrefix(strings.ToLower(line), "content-length:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				if cl, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
					contentLength = cl
				}
			}
			break
		}
	}

	// Read body if Content-Length is specified
	if contentLength > 0 {
		body := make([]byte, contentLength)
		_, err := io.ReadFull(reader, body)
		if err != nil {
			return nil, fmt.Errorf("failed to read response body: %w", err)
		}
		responseData = append(responseData, body...)
	}

	return responseData, nil
}

// Message framing for HTTP messages
func (hpp *HTTPProxyProtocol) sendMessage(data []byte) error {
	// Send length prefix (4 bytes, big-endian)
	length := uint32(len(data))
	if err := binary.Write(hpp.conn, binary.BigEndian, length); err != nil {
		return err
	}
	// Send data
	_, err := hpp.conn.Write(data)
	return err
}

func (hpp *HTTPProxyProtocol) readMessage(buffer []byte) ([]byte, error) {
	// Read length prefix (4 bytes, big-endian)
	var length uint32
	if err := binary.Read(hpp.conn, binary.BigEndian, &length); err != nil {
		return nil, err
	}

	// Read data
	if int(length) > len(buffer) {
		buffer = make([]byte, length)
	}
	n, err := io.ReadFull(hpp.conn, buffer[:length])
	if err != nil {
		return nil, err
	}

	return buffer[:n], nil
}
