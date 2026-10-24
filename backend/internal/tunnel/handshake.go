package tunnel

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"

	"github.com/coder/websocket"
)

const (
	// maxHelloSize bounds the hello message, read before the session exists.
	maxHelloSize = 4 << 10
	maxVersion   = 64
	maxHostname  = 253
)

// hello is the wire form of Hello, sent as one text message by each side: the agent
// first, then the server's answer, which carries Error instead when it refuses.
type hello struct {
	Protocol int    `json:"protocol"`
	Version  string `json:"version"`
	Hostname string `json:"hostname,omitempty"`
	Error    string `json:"error,omitempty"`
}

// Accept runs the server side of the handshake on an accepted WebSocket: it reads the
// agent's hello, refuses it on a protocol mismatch or bad fields, answers with self and
// returns the session, whose lifetime ctx bounds. On error the WebSocket is closed.
func Accept(ctx context.Context, ws *websocket.Conn, self Hello, cfg Config) (*Session, error) {
	cfg = cfg.withDefaults()
	hctx, cancel := context.WithTimeout(ctx, cfg.HandshakeTimeout)
	defer cancel()

	peer, err := readHello(hctx, ws)
	if err == nil {
		err = checkHello(peer, ProtocolVersion)
	}
	if err != nil {
		// Tell the agent why before hanging up; a failed write changes nothing.
		_ = writeHello(hctx, ws, hello{Protocol: ProtocolVersion, Version: self.Version, Error: err.Error()})
		_ = ws.Close(websocket.StatusPolicyViolation, "handshake failed")
		return nil, fmt.Errorf("agent hello: %w", err)
	}
	if err := writeHello(hctx, ws, hello{Protocol: ProtocolVersion, Version: self.Version}); err != nil {
		_ = ws.CloseNow()
		return nil, fmt.Errorf("server hello: %w", err)
	}
	return start(ctx, ws, true, Hello{Version: peer.Version, Hostname: peer.Hostname}, cfg)
}

// Connect runs the agent side of the handshake on a dialed WebSocket: it sends self,
// reads the server's answer and returns the session, whose lifetime ctx bounds. A refusal
// comes back as an error with the server's reason. On error the WebSocket is closed.
func Connect(ctx context.Context, ws *websocket.Conn, self Hello, cfg Config) (*Session, error) {
	return connect(ctx, ws, self, ProtocolVersion, cfg)
}

func connect(ctx context.Context, ws *websocket.Conn, self Hello, protocol int, cfg Config) (*Session, error) {
	cfg = cfg.withDefaults()
	hctx, cancel := context.WithTimeout(ctx, cfg.HandshakeTimeout)
	defer cancel()

	if err := writeHello(hctx, ws, hello{Protocol: protocol, Version: self.Version, Hostname: self.Hostname}); err != nil {
		_ = ws.CloseNow()
		return nil, fmt.Errorf("agent hello: %w", err)
	}
	peer, err := readHello(hctx, ws)
	if err != nil {
		_ = ws.CloseNow()
		return nil, fmt.Errorf("server hello: %w", err)
	}
	if peer.Error != "" {
		_ = ws.CloseNow()
		return nil, fmt.Errorf("server refused the agent: %s", sanitize(peer.Error, 512))
	}
	if err := checkHello(peer, protocol); err != nil {
		_ = ws.Close(websocket.StatusPolicyViolation, "handshake failed")
		return nil, fmt.Errorf("server hello: %w", err)
	}
	return start(ctx, ws, false, Hello{Version: peer.Version}, cfg)
}

func start(ctx context.Context, ws *websocket.Conn, client bool, peer Hello, cfg Config) (*Session, error) {
	// NetConn lifts the read limit; it reads messages as a stream, so that costs no memory.
	return newSession(ctx, websocket.NetConn(ctx, ws, websocket.MessageBinary), client, peer, cfg)
}

func readHello(ctx context.Context, ws *websocket.Conn) (hello, error) {
	ws.SetReadLimit(maxHelloSize)
	typ, data, err := ws.Read(ctx)
	if err != nil {
		return hello{}, fmt.Errorf("read: %w", err)
	}
	if typ != websocket.MessageText {
		return hello{}, errors.New("not a text message")
	}
	var h hello
	if err := json.Unmarshal(data, &h); err != nil {
		return hello{}, errors.New("not a hello message")
	}
	return h, nil
}

func writeHello(ctx context.Context, ws *websocket.Conn, h hello) error {
	data, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("encode: %w", err)
	}
	if err := ws.Write(ctx, websocket.MessageText, data); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

// checkHello validates the other side's hello against the protocol this side speaks.
// The fields end up in logs and the UI, so they must be short and printable.
func checkHello(h hello, protocol int) error {
	if h.Protocol != protocol {
		return fmt.Errorf("tunnel protocol mismatch: peer speaks version %d, this side speaks %d; run the same nudge version on server and agent", h.Protocol, protocol)
	}
	if !printable(h.Version, maxVersion) {
		return fmt.Errorf("invalid version: want up to %d printable characters", maxVersion)
	}
	if !printable(h.Hostname, maxHostname) {
		return fmt.Errorf("invalid hostname: want up to %d printable characters", maxHostname)
	}
	return nil
}

func printable(s string, maxLen int) bool {
	return len(s) <= maxLen && strings.IndexFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) < 0
}

// sanitize shortens text from the peer and drops unprintable runes before it is shown.
func sanitize(s string, maxLen int) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsPrint(r) {
			return r
		}
		return -1
	}, s)
	if len(s) > maxLen {
		s = strings.ToValidUTF8(s[:maxLen], "")
	}
	return s
}
