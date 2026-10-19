package credentials

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const (
	maxMessage     = 1 << 20
	requestTimeout = 2 * time.Minute
)

// signingProxy keeps one upstream connection for every card. It accepts only
// identity listing and Git SSHSIG signing, never SSH authentication or key edits.
type signingProxy struct {
	source   string
	requests chan struct{}
	mu       sync.Mutex
	closed   bool
	upstream net.Conn
	clients  map[net.Conn]struct{}
}

func (p *signingProxy) serve(ctx context.Context, listener net.Listener) {
	for {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			_ = conn.Close()
			return
		}
		p.clients[conn] = struct{}{}
		p.mu.Unlock()
		go p.handle(ctx, conn)
	}
}

func (p *signingProxy) handle(ctx context.Context, conn net.Conn) {
	defer func() {
		_ = conn.Close()
		p.mu.Lock()
		delete(p.clients, conn)
		p.mu.Unlock()
	}()
	for {
		_ = conn.SetDeadline(time.Now().Add(requestTimeout))
		request, err := readMessage(conn)
		if err != nil {
			return
		}
		ctx, cancel := context.WithTimeout(ctx, requestTimeout)
		deadline, _ := ctx.Deadline()
		_ = conn.SetDeadline(deadline)
		response := []byte{5} // SSH_AGENT_FAILURE
		if permitted(request) {
			if reply, err := p.exchange(ctx, request); err == nil {
				response = reply
			}
		}
		cancel()
		if err := writeMessage(conn, response); err != nil {
			return
		}
	}
}

func (p *signingProxy) exchange(ctx context.Context, request []byte) ([]byte, error) {
	select {
	case p.requests <- struct{}{}:
		defer func() { <-p.requests }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	response, err := p.exchangeOnce(ctx, request)
	// Listing keys is safe to replay after a stale connection. Signing is not:
	// a lost response does not mean the signature (or approval) never happened.
	if err != nil && len(request) == 1 && request[0] == 11 {
		return p.exchangeOnce(ctx, request)
	}
	return response, err
}

// exchangeOnce runs with exclusive ownership of p.requests.
func (p *signingProxy) exchangeOnce(ctx context.Context, request []byte) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, net.ErrClosed
	}
	conn := p.upstream
	p.mu.Unlock()
	if conn == nil {
		var err error
		conn, err = (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", p.source)
		if err != nil {
			return nil, err
		}
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			_ = conn.Close()
			return nil, net.ErrClosed
		}
		p.upstream = conn
		p.mu.Unlock()
	}
	deadline, _ := ctx.Deadline()
	_ = conn.SetDeadline(deadline)
	err := writeMessage(conn, request)
	var response []byte
	if err == nil {
		response, err = readMessage(conn)
	}
	if err != nil || (len(response) == 1 && response[0] == 5) {
		// A rejected request can reflect expired authorization on this connection.
		// Return the failure, but let the next request establish a fresh session.
		_ = conn.Close()
		p.mu.Lock()
		p.upstream = nil
		p.mu.Unlock()
	}
	return response, err
}

func (p *signingProxy) close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	if p.upstream != nil {
		_ = p.upstream.Close()
	}
	for conn := range p.clients {
		_ = conn.Close()
	}
}

func readMessage(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(header[:])
	if n == 0 || n > maxMessage {
		return nil, errors.New("invalid SSH agent message size")
	}
	message := make([]byte, n)
	_, err := io.ReadFull(r, message)
	return message, err
}

func writeMessage(w io.Writer, message []byte) error {
	var header [4]byte
	binary.BigEndian.PutUint32(header[:], uint32(len(message)))
	_, err := io.Copy(w, io.MultiReader(bytes.NewReader(header[:]), bytes.NewReader(message)))
	return err
}

func sshString(data []byte) ([]byte, []byte, error) {
	if len(data) < 4 {
		return nil, nil, io.ErrUnexpectedEOF
	}
	n := binary.BigEndian.Uint32(data[:4])
	if uint64(n) > uint64(len(data)-4) {
		return nil, nil, io.ErrUnexpectedEOF
	}
	return data[4 : 4+int(n)], data[4+int(n):], nil
}

func permitted(message []byte) bool {
	if len(message) == 1 && message[0] == 11 { // SSH_AGENTC_REQUEST_IDENTITIES
		return true
	}
	if len(message) == 0 || message[0] != 13 { // SSH_AGENTC_SIGN_REQUEST
		return false
	}
	_, rest, err := sshString(message[1:]) // public key
	if err != nil {
		return false
	}
	data, flags, err := sshString(rest)
	if err != nil || len(flags) != 4 || !bytes.HasPrefix(data, []byte("SSHSIG")) {
		return false
	}
	namespace, rest, err := sshString(data[6:])
	if err != nil || string(namespace) != "git" {
		return false
	}
	reserved, rest, err := sshString(rest)
	if err != nil || len(reserved) != 0 {
		return false
	}
	hash, rest, err := sshString(rest)
	if err != nil {
		return false
	}
	digest, rest, err := sshString(rest)
	return err == nil && len(rest) == 0 && ((string(hash) == "sha256" && len(digest) == 32) || (string(hash) == "sha512" && len(digest) == 64))
}

func listenSocket(path string) (*net.UnixListener, error) {
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("listen on credential socket: %w", err)
	}
	return listener, nil
}
