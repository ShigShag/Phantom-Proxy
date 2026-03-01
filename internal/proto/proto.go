package proto

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"time"

	"github.com/ShigShag/Phantom-Proxy/internal/buildcfg"
	"github.com/ShigShag/Phantom-Proxy/internal/crypto"
)

// Message types for the control protocol.
const (
	AuthChallenge byte = 0x01
	AuthResponse  byte = 0x02
	AuthOK        byte = 0x03
	AuthFail      byte = 0x04
	ClientInfo    byte = 0x05

	Keepalive    byte = 0x10
	KeepaliveAck byte = 0x11

	Connect    byte = 0x20
	ConnectAck byte = 0x21

	PortFwdReq byte = 0x30
	PortFwdAck byte = 0x31

	Disconnect byte = 0xFF
)

// Message is the wire-format envelope: [1B type][4B len][payload].
type Message struct {
	Type    byte
	Payload []byte
}

// ConnectPayload is the body of a CONNECT message.
type ConnectPayload struct {
	Addr string `json:"addr"`
}

// ConnectAckPayload is the body of a CONNECT_ACK message.
type ConnectAckPayload struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// PortFwdPayload describes a port-forward request.
type PortFwdPayload struct {
	Direction string `json:"direction"` // "local" or "remote"
	Bind      string `json:"bind"`
	Target    string `json:"target"`
}

// PortFwdAckPayload is the response to a port-forward request.
type PortFwdAckPayload struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
}

// ClientInfoPayload carries metadata about the client.
type ClientInfoPayload struct {
	Hostname string `json:"hostname"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
}

// WriteMessage writes a framed message to the writer.
func WriteMessage(w io.Writer, msg *Message) error {
	header := make([]byte, 5)
	header[0] = msg.Type
	binary.BigEndian.PutUint32(header[1:5], uint32(len(msg.Payload)))
	if _, err := w.Write(header); err != nil {
		return err
	}
	if len(msg.Payload) > 0 {
		_, err := w.Write(msg.Payload)
		return err
	}
	return nil
}

// ReadMessage reads a framed message from the reader.
func ReadMessage(r io.Reader) (*Message, error) {
	header := make([]byte, 5)
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, err
	}
	msgType := header[0]
	length := binary.BigEndian.Uint32(header[1:5])
	if length > 1<<20 { // 1 MiB limit
		return nil, fmt.Errorf("message too large: %d bytes", length)
	}
	payload := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(r, payload); err != nil {
			return nil, err
		}
	}
	return &Message{Type: msgType, Payload: payload}, nil
}

// WriteConnect sends a CONNECT message with the target address.
func WriteConnect(w io.Writer, addr string) error {
	payload, _ := json.Marshal(ConnectPayload{Addr: addr})
	return WriteMessage(w, &Message{Type: Connect, Payload: payload})
}

// ReadConnect reads and parses a CONNECT message.
func ReadConnect(r io.Reader) (string, error) {
	msg, err := ReadMessage(r)
	if err != nil {
		return "", err
	}
	if msg.Type != Connect {
		return "", fmt.Errorf("expected CONNECT (0x%02x), got 0x%02x", Connect, msg.Type)
	}
	var p ConnectPayload
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		return "", fmt.Errorf("decode CONNECT: %w", err)
	}
	return p.Addr, nil
}

// WriteConnectAck sends a CONNECT_ACK message.
func WriteConnectAck(w io.Writer, ok bool, errMsg string) error {
	payload, _ := json.Marshal(ConnectAckPayload{OK: ok, Error: errMsg})
	return WriteMessage(w, &Message{Type: ConnectAck, Payload: payload})
}

// ReadConnectAck reads and parses a CONNECT_ACK message.
func ReadConnectAck(r io.Reader) (bool, string, error) {
	msg, err := ReadMessage(r)
	if err != nil {
		return false, "", err
	}
	if msg.Type != ConnectAck {
		return false, "", fmt.Errorf("expected CONNECT_ACK (0x%02x), got 0x%02x", ConnectAck, msg.Type)
	}
	var p ConnectAckPayload
	if err := json.Unmarshal(msg.Payload, &p); err != nil {
		return false, "", fmt.Errorf("decode CONNECT_ACK: %w", err)
	}
	return p.OK, p.Error, nil
}

// ServerHandshake performs the server side of the HMAC challenge-response auth.
// It opens a control stream (stream 0), sends a nonce challenge, and verifies the client's HMAC.
func ServerHandshake(ctrl net.Conn, secret string) (*ClientInfoPayload, error) {
	// Generate and send nonce challenge.
	nonce, err := crypto.GenerateNonce()
	if err != nil {
		return nil, err
	}
	if err := WriteMessage(ctrl, &Message{Type: AuthChallenge, Payload: nonce}); err != nil {
		return nil, fmt.Errorf("send challenge: %w", err)
	}

	// Read HMAC response.
	resp, err := ReadMessage(ctrl)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if resp.Type != AuthResponse {
		return nil, fmt.Errorf("expected AUTH_RESPONSE, got 0x%02x", resp.Type)
	}

	// Verify.
	key := crypto.DeriveKey(secret, crypto.DeterministicSalt(buildcfg.AuthLabel))
	if !crypto.VerifyHMAC(key, nonce, resp.Payload) {
		WriteMessage(ctrl, &Message{Type: AuthFail})
		return nil, fmt.Errorf("authentication failed")
	}

	// Send OK.
	if err := WriteMessage(ctrl, &Message{Type: AuthOK}); err != nil {
		return nil, fmt.Errorf("send auth ok: %w", err)
	}

	// Read client info.
	infoMsg, err := ReadMessage(ctrl)
	if err != nil {
		return nil, fmt.Errorf("read client info: %w", err)
	}
	if infoMsg.Type != ClientInfo {
		return nil, fmt.Errorf("expected CLIENT_INFO, got 0x%02x", infoMsg.Type)
	}
	var info ClientInfoPayload
	if err := json.Unmarshal(infoMsg.Payload, &info); err != nil {
		return nil, fmt.Errorf("decode client info: %w", err)
	}

	return &info, nil
}

// ClientHandshake performs the client side of the HMAC challenge-response auth.
func ClientHandshake(ctrl net.Conn, secret string, info *ClientInfoPayload) error {
	// Read nonce challenge.
	challenge, err := ReadMessage(ctrl)
	if err != nil {
		return fmt.Errorf("read challenge: %w", err)
	}
	if challenge.Type != AuthChallenge {
		return fmt.Errorf("expected AUTH_CHALLENGE, got 0x%02x", challenge.Type)
	}

	// Compute HMAC and send response.
	key := crypto.DeriveKey(secret, crypto.DeterministicSalt(buildcfg.AuthLabel))
	mac := crypto.ComputeHMAC(key, challenge.Payload)
	if err := WriteMessage(ctrl, &Message{Type: AuthResponse, Payload: mac}); err != nil {
		return fmt.Errorf("send response: %w", err)
	}

	// Read result.
	result, err := ReadMessage(ctrl)
	if err != nil {
		return fmt.Errorf("read result: %w", err)
	}
	if result.Type == AuthFail {
		return fmt.Errorf("authentication failed: bad secret")
	}
	if result.Type != AuthOK {
		return fmt.Errorf("expected AUTH_OK, got 0x%02x", result.Type)
	}

	// Send client info.
	payload, _ := json.Marshal(info)
	return WriteMessage(ctrl, &Message{Type: ClientInfo, Payload: payload})
}

// RunKeepalive sends periodic keepalive messages on the control stream.
// It stops when the context is cancelled or a write fails.
func RunKeepalive(ctx context.Context, ctrl net.Conn, logger *slog.Logger) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Send DISCONNECT on graceful shutdown.
			WriteMessage(ctrl, &Message{Type: Disconnect})
			return
		case <-ticker.C:
			if err := WriteMessage(ctrl, &Message{Type: Keepalive}); err != nil {
				logger.Debug("keepalive send failed", "error", err)
				return
			}
		}
	}
}

// HandleKeepalive processes keepalive messages on the server's control stream.
// Returns when the control stream is closed or a DISCONNECT is received.
func HandleKeepalive(ctx context.Context, ctrl net.Conn, logger *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		msg, err := ReadMessage(ctrl)
		if err != nil {
			logger.Debug("control stream closed", "error", err)
			return
		}

		switch msg.Type {
		case Keepalive:
			WriteMessage(ctrl, &Message{Type: KeepaliveAck})
		case Disconnect:
			logger.Info("client sent DISCONNECT")
			return
		default:
			logger.Debug("unexpected control message", "type", fmt.Sprintf("0x%02x", msg.Type))
		}
	}
}
