package p2p

import (
	"bufio"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
)

type HTTPProxyProtocol struct {
	conn   *Conn
	logger *slog.Logger
}

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

func (hpp *HTTPProxyProtocol) Serve(ctx context.Context, tcpAddr string) error {
	hpp.logger.Info("Starting HTTP proxy server", "tcp_addr", tcpAddr)

	buffer := make([]byte, 8192)

	for {
		select {
		case <-ctx.Done():
			hpp.logger.Info("HTTP proxy server shutting down")
			return nil
		default:
		}

		requestData, err := hpp.readMessage(buffer)
		if err != nil {
			if err == io.EOF {
				hpp.logger.Info("WebRTC connection closed")
				return nil
			}
			return fmt.Errorf("failed to read HTTP request from WebRTC: %w", err)
		}

		hpp.logger.Info("Received HTTP request from WebRTC", "bytes", len(requestData))

		tcpConn, err := net.Dial("tcp", tcpAddr)
		if err != nil {
			hpp.logger.Error("Failed to connect to web server", "addr", tcpAddr, "error", err)
			continue
		}

		if _, err := tcpConn.Write(requestData); err != nil {
			hpp.logger.Error("Failed to send request to web server", "error", err)
			tcpConn.Close()
			continue
		}

		responseData, err := hpp.readHTTPResponse(tcpConn)
		if err != nil {
			hpp.logger.Error("Failed to read response from web server", "error", err)
			tcpConn.Close()
			continue
		}

		tcpConn.Close()

		hpp.logger.Info("Received HTTP response from web server", "bytes", len(responseData))

		if err := hpp.sendMessage(responseData); err != nil {
			return fmt.Errorf("failed to send response over WebRTC: %w", err)
		}

		hpp.logger.Info("HTTP request-response cycle completed")
	}
}

// Connect starts the HTTP proxy client (sender side)
func (hpp *HTTPProxyProtocol) Connect(ctx context.Context, tcpAddr string) error {
	hpp.logger.Info("Starting HTTP proxy client", "tcp_addr", tcpAddr)

	// Start TCP listener
	listener, err := net.Listen("tcp", tcpAddr)
	if err != nil {
		return fmt.Errorf("failed to listen on %s: %w", tcpAddr, err)
	}
	defer listener.Close()

	hpp.logger.Info("TCP listener started", "addr", tcpAddr)

	done := make(chan struct{})
	defer close(done)

	go func() {
		select {
		case <-ctx.Done():
			listener.Close()
		case <-done:
			return
		}
	}()

	for {
		select {
		case <-ctx.Done():
			hpp.logger.Info("HTTP proxy client shutting down")
			return nil
		default:
		}

		tcpConn, err := listener.Accept()
		if err != nil {
			select {
			case <-ctx.Done():
				hpp.logger.Info("HTTP proxy client shutting down")
				return nil
			default:
				hpp.logger.Error("Failed to accept TCP connection", "error", err)
				continue
			}
		}

		hpp.logger.Info("Accepted client connection", "remote_addr", tcpConn.RemoteAddr())
		hpp.handleClientRequest(tcpConn)
		hpp.logger.Info("Client request completed, waiting for next connection")
	}
}

func (hpp *HTTPProxyProtocol) handleClientRequest(tcpConn net.Conn) {
	defer tcpConn.Close()

	requestData, err := hpp.readHTTPRequest(tcpConn)
	if err != nil {
		hpp.logger.Error("Failed to read HTTP request from client", "error", err)
		return
	}

	hpp.logger.Info("Received HTTP request from client", "bytes", len(requestData))

	if err := hpp.sendMessage(requestData); err != nil {
		hpp.logger.Error("Failed to send request over WebRTC", "error", err)
		return
	}

	buffer := make([]byte, 8192)
	responseData, err := hpp.readMessage(buffer)
	if err != nil {
		hpp.logger.Error("Failed to read response from WebRTC", "error", err)
		return
	}

	hpp.logger.Info("Received HTTP response from WebRTC", "bytes", len(responseData))

	if _, err := tcpConn.Write(responseData); err != nil {
		hpp.logger.Error("Failed to send response to client", "error", err)
		return
	}

	hpp.logger.Info("HTTP request-response cycle completed for client")
}

func (hpp *HTTPProxyProtocol) readHTTPRequest(conn net.Conn) ([]byte, error) {
	reader := bufio.NewReader(conn)
	var requestData []byte

	requestLine, err := reader.ReadString('\n')
	if err != nil {
		return nil, fmt.Errorf("failed to read request line: %w", err)
	}
	requestData = append(requestData, []byte(requestLine)...)

	headersEnded := false
	for !headersEnded {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("failed to read request header: %w", err)
		}
		requestData = append(requestData, []byte(line)...)

		if line == "\r\n" {
			headersEnded = true
		}
	}

	contentLength := -1
	transferEncoding := ""
	headersStr := string(requestData)
	for _, line := range strings.Split(headersStr, "\r\n") {
		lowerLine := strings.ToLower(line)
		if strings.HasPrefix(lowerLine, "content-length:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				if cl, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
					contentLength = cl
				}
			}
		} else if strings.HasPrefix(lowerLine, "transfer-encoding:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				transferEncoding = strings.ToLower(strings.TrimSpace(parts[1]))
			}
		}
	}

	if transferEncoding == "chunked" {
		body, err := hpp.readChunkedBody(reader)
		if err != nil {
			return nil, fmt.Errorf("failed to read chunked request body: %w", err)
		}
		requestData = append(requestData, body...)
	} else if contentLength > 0 {
		body := make([]byte, contentLength)
		_, err := io.ReadFull(reader, body)
		if err != nil {
			return nil, fmt.Errorf("failed to read request body: %w", err)
		}
		requestData = append(requestData, body...)
	}

	return requestData, nil
}

func (hpp *HTTPProxyProtocol) readHTTPResponse(conn net.Conn) ([]byte, error) {
	reader := bufio.NewReader(conn)
	var responseData []byte

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

	// Parse Content-Length and Transfer-Encoding
	contentLength := -1
	transferEncoding := ""
	headersStr := string(responseData)
	for _, line := range strings.Split(headersStr, "\r\n") {
		lowerLine := strings.ToLower(line)
		if strings.HasPrefix(lowerLine, "content-length:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				if cl, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
					contentLength = cl
				}
			}
		} else if strings.HasPrefix(lowerLine, "transfer-encoding:") {
			parts := strings.SplitN(line, ":", 2)
			if len(parts) == 2 {
				transferEncoding = strings.ToLower(strings.TrimSpace(parts[1]))
			}
		}
	}

	if transferEncoding == "chunked" {
		body, err := hpp.readChunkedBody(reader)
		if err != nil {
			return nil, fmt.Errorf("failed to read chunked response body: %w", err)
		}
		responseData = append(responseData, body...)
	} else if contentLength > 0 {
		body := make([]byte, contentLength)
		_, err := io.ReadFull(reader, body)
		if err != nil {
			return nil, fmt.Errorf("failed to read response body: %w", err)
		}
		responseData = append(responseData, body...)
	}

	return responseData, nil
}

func (hpp *HTTPProxyProtocol) readChunkedBody(reader *bufio.Reader) ([]byte, error) {
	var body []byte

	for {
		sizeLine, err := reader.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("failed to read chunk size: %w", err)
		}

		sizeStr := strings.TrimSpace(sizeLine)
		if sizeStr == "" {
			continue // Skip empty lines
		}

		size, err := strconv.ParseInt(sizeStr, 16, 64)
		if err != nil {
			return nil, fmt.Errorf("invalid chunk size: %s", sizeStr)
		}

		if size == 0 {
			break
		}

		chunkData := make([]byte, size)
		_, err = io.ReadFull(reader, chunkData)
		if err != nil {
			return nil, fmt.Errorf("failed to read chunk data: %w", err)
		}
		body = append(body, chunkData...)

		crlf := make([]byte, 2)
		_, err = io.ReadFull(reader, crlf)
		if err != nil {
			return nil, fmt.Errorf("failed to read chunk trailer: %w", err)
		}
		if crlf[0] != '\r' || crlf[1] != '\n' {
			return nil, fmt.Errorf("invalid chunk trailer: %v", crlf)
		}
	}

	var trailingHeaders []byte
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			return nil, fmt.Errorf("failed to read trailing headers: %w", err)
		}
		if line == "\r\n" {
			break
		}
		trailingHeaders = append(trailingHeaders, []byte(line)...)
	}

	if len(trailingHeaders) > 0 {
		body = append(body, trailingHeaders...)
	}

	return body, nil
}

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
