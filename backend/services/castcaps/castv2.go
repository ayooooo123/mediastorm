// Package castcaps discovers what a Cast receiver can actually play.
//
// Receivers lie by omission: every one of them accepts a LOAD for a stream it
// cannot decode, fetches a few segments, and then quietly lands in IDLE/ERROR.
// There is no capability query in the Cast protocol, so the only reliable
// answer comes from loading a tiny probe stream and watching the playhead. That
// costs a few seconds, which is why results are cached per receiver and the
// probe never runs on the path that starts a real cast.
package castcaps

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"time"
)

const (
	namespaceConnection = "urn:x-cast:com.google.cast.tp.connection"
	namespaceHeartbeat  = "urn:x-cast:com.google.cast.tp.heartbeat"
	namespaceReceiver   = "urn:x-cast:com.google.cast.receiver"
	namespaceMedia      = "urn:x-cast:com.google.cast.media"

	defaultReceiverID = "receiver-0"
	defaultMediaApp   = "CC1AD845" // Default Media Receiver
	castPort          = 8009
)

// castMessage is the CASTV2 envelope. The protocol only ever needs string
// payloads, so the protobuf is encoded by hand rather than pulling in a
// generated package for six fields.
type castMessage struct {
	SourceID      string
	DestinationID string
	Namespace     string
	Payload       string
}

func (m castMessage) encode() []byte {
	var body []byte
	body = appendVarintField(body, 1, 0) // protocol_version = CASTV2_1_0
	body = appendStringField(body, 2, m.SourceID)
	body = appendStringField(body, 3, m.DestinationID)
	body = appendStringField(body, 4, m.Namespace)
	body = appendVarintField(body, 5, 0) // payload_type = STRING
	body = appendStringField(body, 6, m.Payload)

	framed := make([]byte, 4+len(body))
	binary.BigEndian.PutUint32(framed, uint32(len(body)))
	copy(framed[4:], body)
	return framed
}

func decodeCastMessage(body []byte) (castMessage, error) {
	var msg castMessage
	for len(body) > 0 {
		tag, n := binary.Uvarint(body)
		if n <= 0 {
			return msg, fmt.Errorf("bad field tag")
		}
		body = body[n:]
		field := tag >> 3
		switch tag & 0x7 {
		case 0: // varint
			_, n := binary.Uvarint(body)
			if n <= 0 {
				return msg, fmt.Errorf("bad varint in field %d", field)
			}
			body = body[n:]
		case 2: // length-delimited
			length, n := binary.Uvarint(body)
			if n <= 0 || uint64(len(body[n:])) < length {
				return msg, fmt.Errorf("bad length in field %d", field)
			}
			value := string(body[n : n+int(length)])
			body = body[n+int(length):]
			switch field {
			case 2:
				msg.SourceID = value
			case 3:
				msg.DestinationID = value
			case 4:
				msg.Namespace = value
			case 6:
				msg.Payload = value
			}
		default:
			return msg, fmt.Errorf("unsupported wire type in field %d", field)
		}
	}
	return msg, nil
}

func appendVarintField(dst []byte, field int, value uint64) []byte {
	dst = binary.AppendUvarint(dst, uint64(field)<<3)
	return binary.AppendUvarint(dst, value)
}

func appendStringField(dst []byte, field int, value string) []byte {
	dst = binary.AppendUvarint(dst, uint64(field)<<3|2)
	dst = binary.AppendUvarint(dst, uint64(len(value)))
	return append(dst, value...)
}

// conn is a minimal CASTV2 sender: enough to launch the Default Media
// Receiver, load a stream, and read media status.
type conn struct {
	tls      *tls.Conn
	sourceID string

	writeMu sync.Mutex

	requestID int
	incoming  chan castMessage
	closeOnce sync.Once
	done      chan struct{}
	readErr   error
}

func dial(ctx context.Context, host string) (*conn, error) {
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	raw, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, fmt.Sprint(castPort)))
	if err != nil {
		return nil, fmt.Errorf("dial receiver: %w", err)
	}
	// Receivers present a self-signed certificate, and older Cast firmware
	// stalls on a TLS 1.3 ClientHello.
	tlsConn := tls.Client(raw, &tls.Config{
		InsecureSkipVerify: true, // #nosec G402 - device-local self-signed cert
		MaxVersion:         tls.VersionTLS12,
	})
	handshakeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := tlsConn.HandshakeContext(handshakeCtx); err != nil {
		_ = raw.Close()
		return nil, fmt.Errorf("tls handshake: %w", err)
	}

	c := &conn{
		tls:      tlsConn,
		sourceID: fmt.Sprintf("sender-mediastorm-%d", time.Now().UnixNano()%100000),
		incoming: make(chan castMessage, 64),
		done:     make(chan struct{}),
	}
	go c.readLoop()
	go c.heartbeatLoop()
	return c, nil
}

func (c *conn) readLoop() {
	defer close(c.incoming)
	header := make([]byte, 4)
	for {
		if _, err := io.ReadFull(c.tls, header); err != nil {
			c.readErr = err
			return
		}
		size := binary.BigEndian.Uint32(header)
		if size == 0 || size > 1<<20 {
			c.readErr = fmt.Errorf("implausible frame size %d", size)
			return
		}
		body := make([]byte, size)
		if _, err := io.ReadFull(c.tls, body); err != nil {
			c.readErr = err
			return
		}
		msg, err := decodeCastMessage(body)
		if err != nil {
			continue
		}
		if msg.Namespace == namespaceHeartbeat && strings.Contains(msg.Payload, "PING") {
			_ = c.send(castMessage{
				SourceID:      c.sourceID,
				DestinationID: msg.SourceID,
				Namespace:     namespaceHeartbeat,
				Payload:       `{"type":"PONG"}`,
			})
			continue
		}
		select {
		case c.incoming <- msg:
		case <-c.done:
			return
		default: // drop rather than stall the reader
		}
	}
}

func (c *conn) heartbeatLoop() {
	ticker := time.NewTicker(4 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			_ = c.send(castMessage{
				SourceID:      c.sourceID,
				DestinationID: defaultReceiverID,
				Namespace:     namespaceHeartbeat,
				Payload:       `{"type":"PING"}`,
			})
		}
	}
}

func (c *conn) send(msg castMessage) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.tls.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := c.tls.Write(msg.encode())
	return err
}

func (c *conn) nextRequestID() int {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.requestID++
	return c.requestID
}

func (c *conn) connectTo(destination string) error {
	return c.send(castMessage{
		SourceID:      c.sourceID,
		DestinationID: destination,
		Namespace:     namespaceConnection,
		Payload:       `{"type":"CONNECT"}`,
	})
}

func (c *conn) close() {
	c.closeOnce.Do(func() {
		close(c.done)
		_ = c.tls.Close()
	})
}

type receiverStatus struct {
	Type   string `json:"type"`
	Status struct {
		Applications []struct {
			AppID       string `json:"appId"`
			SessionID   string `json:"sessionId"`
			TransportID string `json:"transportId"`
			StatusText  string `json:"statusText"`
		} `json:"applications"`
	} `json:"status"`
}

type mediaStatusEnvelope struct {
	Type   string `json:"type"`
	Status []struct {
		MediaSessionID int     `json:"mediaSessionId"`
		PlayerState    string  `json:"playerState"`
		IdleReason     string  `json:"idleReason"`
		CurrentTime    float64 `json:"currentTime"`
		Media          struct {
			ContentID string `json:"contentId"`
		} `json:"media"`
	} `json:"status"`
}

// launchMediaReceiver starts (or joins) the Default Media Receiver and returns
// its transport id.
func (c *conn) launchMediaReceiver(ctx context.Context) (string, error) {
	if err := c.connectTo(defaultReceiverID); err != nil {
		return "", fmt.Errorf("connect to receiver: %w", err)
	}
	payload := fmt.Sprintf(`{"type":"LAUNCH","appId":%q,"requestId":%d}`, defaultMediaApp, c.nextRequestID())
	if err := c.send(castMessage{
		SourceID:      c.sourceID,
		DestinationID: defaultReceiverID,
		Namespace:     namespaceReceiver,
		Payload:       payload,
	}); err != nil {
		return "", fmt.Errorf("launch: %w", err)
	}

	deadline := time.After(20 * time.Second)
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline:
			return "", fmt.Errorf("receiver did not report the media app")
		case msg, ok := <-c.incoming:
			if !ok {
				return "", fmt.Errorf("connection closed: %v", c.readErr)
			}
			if msg.Namespace != namespaceReceiver {
				continue
			}
			var status receiverStatus
			if json.Unmarshal([]byte(msg.Payload), &status) != nil {
				continue
			}
			for _, app := range status.Status.Applications {
				if app.AppID == defaultMediaApp && app.TransportID != "" {
					return app.TransportID, nil
				}
			}
		}
	}
}

func (c *conn) stopApp(sessionID string) {
	if sessionID == "" {
		return
	}
	_ = c.send(castMessage{
		SourceID:      c.sourceID,
		DestinationID: defaultReceiverID,
		Namespace:     namespaceReceiver,
		Payload:       fmt.Sprintf(`{"type":"STOP","sessionId":%q,"requestId":%d}`, sessionID, c.nextRequestID()),
	})
}
