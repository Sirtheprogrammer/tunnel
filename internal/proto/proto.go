// Package proto defines the wire contract between the tunnelx agent and the
// tunnelxd server.
//
// An agent opens a single TLS/TCP connection and layers a yamux session over
// it. Stream 0 (the first stream the agent opens) is the control channel and
// carries newline-delimited JSON envelopes for the lifetime of the session.
// Every public HTTP request the server receives is forwarded on a fresh stream
// that the *server* opens, prefixed by a single-line StreamHeader.
package proto

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Version is the protocol version. The server rejects agents that do not match.
const Version = "1"

// ControlPort is the default port the server listens on for agent connections.
const ControlPort = 7835

// Type identifies the payload carried by an Envelope.
type Type string

const (
	TypeAuth          Type = "auth"
	TypeAuthResp      Type = "auth_resp"
	TypeTunnelCreate  Type = "tunnel.create"
	TypeTunnelCreated Type = "tunnel.created"
	TypeTunnelClose   Type = "tunnel.close"
	TypeError         Type = "error"
	TypePing          Type = "ping"
	TypePong          Type = "pong"
)

// Proto is the tunnelled protocol. Only ProtoHTTP is implemented in v1; the
// field exists so raw TCP forwarding can be added without a wire break.
type Proto string

const (
	ProtoHTTP Proto = "http"
	ProtoTCP  Proto = "tcp"
)

// Envelope is the single message frame on the control stream. The payload is
// carried opaquely in Data and interpreted according to Type.
type Envelope struct {
	Type Type            `json:"type"`
	ID   string          `json:"id,omitempty"` // correlates a response to its request
	Data json.RawMessage `json:"data,omitempty"`
}

// Auth is the first message an agent sends. Token may be empty only if the
// server is running with authentication disabled.
type Auth struct {
	Protocol string `json:"protocol"`
	Token    string `json:"token,omitempty"`
	Version  string `json:"version"`
	OS       string `json:"os"`
	Arch     string `json:"arch"`
	Hostname string `json:"hostname,omitempty"`
}

// AuthResp is the server reply to Auth. A failed auth is reported here and then
// the connection is closed; it is not delivered as TypeError, so an agent has
// exactly one message to wait for.
type AuthResp struct {
	OK        bool   `json:"ok"`
	AccountID string `json:"account_id,omitempty"`
	Version   string `json:"version"`
	Error     string `json:"error,omitempty"`
}

// TunnelCreate requests a public URL. An empty Subdomain asks the server to
// generate a random one.
type TunnelCreate struct {
	Proto     Proto  `json:"proto"`
	Subdomain string `json:"subdomain,omitempty"`
	LocalAddr string `json:"local_addr"`
}

// TunnelCreated is the successful reply to TunnelCreate.
type TunnelCreated struct {
	ID        string `json:"id"`
	Subdomain string `json:"subdomain"`
	URL       string `json:"url"`
}

// TunnelClose asks the server to tear down a tunnel.
type TunnelClose struct {
	ID string `json:"id"`
}

// Error codes carried by ErrorMsg. Agents switch on Code, not Message.
const (
	CodeUnauthorized     = "unauthorized"
	CodeSubdomainTaken   = "subdomain_taken"
	CodeSubdomainInvalid = "subdomain_invalid"
	CodeTunnelLimit      = "tunnel_limit"
	CodeUnsupported      = "unsupported"
	CodeInternal         = "internal"
)

// ErrorMsg reports a failure. When it carries the ID of a prior request it is
// that request's terminal response.
type ErrorMsg struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *ErrorMsg) Error() string { return fmt.Sprintf("%s: %s", e.Code, e.Message) }

// StreamHeader prefixes every data stream the server opens. It is written as a
// single JSON line, immediately followed by the raw HTTP/1.1 request bytes.
type StreamHeader struct {
	TunnelID   string `json:"tunnel_id"`
	RequestID  string `json:"request_id"`
	RemoteAddr string `json:"remote_addr"`
	// Upgrade marks a protocol upgrade (WebSocket): after the response headers
	// the stream becomes an opaque bidirectional pipe rather than carrying a
	// single bounded response.
	Upgrade bool `json:"upgrade,omitempty"`
}

// maxLine bounds a single control message so a malformed or hostile peer cannot
// force an unbounded allocation.
const maxLine = 1 << 20

// ErrLineTooLong is returned when a peer sends a control message over maxLine.
var ErrLineTooLong = errors.New("proto: control message exceeds size limit")

// Conn reads and writes newline-delimited JSON over a stream. One reader and
// one writer may use it concurrently, which is how the control stream is
// driven: a single read loop, plus writes from other goroutines serialised by
// the caller.
type Conn struct {
	r *bufio.Reader
	w io.Writer
}

// NewConn wraps a stream. The returned Conn buffers reads, so the underlying
// stream must not be read directly afterwards.
func NewConn(rw io.ReadWriter) *Conn {
	return &Conn{r: bufio.NewReaderSize(rw, 4096), w: rw}
}

// Reader exposes the buffered reader so a caller can keep reading raw bytes
// after the JSON prefix.
func (c *Conn) Reader() *bufio.Reader { return c.r }

// ReadEnvelope reads one message. It returns io.EOF when the peer closes.
func (c *Conn) ReadEnvelope() (*Envelope, error) {
	line, err := readLine(c.r)
	if err != nil {
		return nil, err
	}
	var e Envelope
	if err := json.Unmarshal(line, &e); err != nil {
		return nil, fmt.Errorf("proto: decode envelope: %w", err)
	}
	return &e, nil
}

// readLine reads one newline-terminated message, enforcing maxLine. A final
// message without a trailing newline is accepted.
func readLine(r *bufio.Reader) ([]byte, error) {
	var buf []byte
	for {
		frag, err := r.ReadSlice('\n')
		if len(buf)+len(frag) > maxLine {
			return nil, ErrLineTooLong
		}
		buf = append(buf, frag...)
		if err == nil {
			return buf, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue // message spans more than one buffer fill
		}
		if errors.Is(err, io.EOF) && len(buf) > 0 {
			return buf, nil
		}
		return nil, err
	}
}

// WriteEnvelope writes one message, marshalling payload into Data.
func (c *Conn) WriteEnvelope(t Type, id string, payload any) error {
	e := Envelope{Type: t, ID: id}
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("proto: encode %s: %w", t, err)
		}
		e.Data = b
	}
	b, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("proto: encode envelope: %w", err)
	}
	if len(b)+1 > maxLine {
		return ErrLineTooLong
	}
	if _, err := c.w.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("proto: write %s: %w", t, err)
	}
	return nil
}

// Decode unmarshals an envelope payload into v.
func (e *Envelope) Decode(v any) error {
	if len(e.Data) == 0 {
		return fmt.Errorf("proto: %s has no payload", e.Type)
	}
	if err := json.Unmarshal(e.Data, v); err != nil {
		return fmt.Errorf("proto: decode %s payload: %w", e.Type, err)
	}
	return nil
}

// WriteStreamHeader writes the JSON line that prefixes a data stream.
func WriteStreamHeader(w io.Writer, h *StreamHeader) error {
	b, err := json.Marshal(h)
	if err != nil {
		return fmt.Errorf("proto: encode stream header: %w", err)
	}
	if _, err := w.Write(append(b, '\n')); err != nil {
		return fmt.Errorf("proto: write stream header: %w", err)
	}
	return nil
}

// ReadStreamHeader reads the JSON line prefixing a data stream. The caller must
// keep using r for the HTTP request that follows, since r has buffered past the
// newline.
func ReadStreamHeader(r *bufio.Reader) (*StreamHeader, error) {
	line, err := readLine(r)
	if err != nil {
		return nil, err
	}
	var h StreamHeader
	if err := json.Unmarshal(line, &h); err != nil {
		return nil, fmt.Errorf("proto: decode stream header: %w", err)
	}
	return &h, nil
}
