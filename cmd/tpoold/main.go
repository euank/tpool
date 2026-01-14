package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"github.com/esk/tpool/internal/config"
	"github.com/esk/tpool/internal/protocol"
	"github.com/esk/tpool/internal/session"
	"github.com/esk/tpool/internal/web"
	"github.com/google/uuid"
)

type Server struct {
	manager  *session.Manager
	sockPath string
	listener net.Listener
}

func NewServer(sockPath string) *Server {
	return &Server{
		manager:  session.NewManager(sockPath),
		sockPath: sockPath,
	}
}

func (s *Server) Start() error {
	os.Remove(s.sockPath)

	listener, err := net.Listen("unix", s.sockPath)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	s.listener = listener

	if err := os.Chmod(s.sockPath, 0700); err != nil {
		return fmt.Errorf("chmod socket: %w", err)
	}

	log.Printf("Daemon listening on %s", s.sockPath)

	for {
		conn, err := listener.Accept()
		if err != nil {
			return err
		}
		go s.handleConnection(conn)
	}
}

func (s *Server) handleConnection(conn net.Conn) {
	defer conn.Close()

	clientID := uuid.New().String()[:8]
	log.Printf("Client %s connected", clientID)
	defer log.Printf("Client %s disconnected", clientID)

	client := &clientHandler{
		server:   s,
		conn:     conn,
		clientID: clientID,
	}
	client.handle()
}

type clientHandler struct {
	server    *Server
	conn      net.Conn
	clientID  string
	attached  *session.Session
	attachMu  sync.Mutex
	writeMu   sync.Mutex
	outputBuf *outputBuffer
}

type outputBuffer struct {
	ch     *clientHandler
	closed bool
	mu     sync.Mutex
}

func (o *outputBuffer) Write(p []byte) (int, error) {
	o.mu.Lock()
	if o.closed {
		o.mu.Unlock()
		return 0, io.ErrClosedPipe
	}
	o.mu.Unlock()

	msg, _ := protocol.NewMessage(protocol.MsgOutput, protocol.OutputPayload{Data: p})
	o.ch.writeMu.Lock()
	err := protocol.WriteMessage(o.ch.conn, msg)
	o.ch.writeMu.Unlock()
	if err != nil {
		return 0, err
	}
	return len(p), nil
}

func (o *outputBuffer) Close() {
	o.mu.Lock()
	o.closed = true
	o.mu.Unlock()
}

func (c *clientHandler) handle() {
	for {
		msg, err := protocol.ReadMessage(c.conn)
		if err != nil {
			return
		}

		if err := c.processMessage(msg); err != nil {
			log.Printf("Client %s error: %v", c.clientID, err)
			c.sendError(err.Error())
		}
	}
}

func (c *clientHandler) processMessage(msg *protocol.Message) error {
	switch msg.Type {
	case protocol.MsgListSessions:
		return c.handleListSessions()

	case protocol.MsgCreateSession:
		return c.handleCreateSession(msg)

	case protocol.MsgDeleteSession:
		return c.handleDeleteSession(msg)

	case protocol.MsgAttach:
		return c.handleAttach(msg)

	case protocol.MsgDetach:
		return c.handleDetach()

	case protocol.MsgInput:
		return c.handleInput(msg)

	case protocol.MsgResize:
		return c.handleResize(msg)

	default:
		return fmt.Errorf("unknown message type: %d", msg.Type)
	}
}

func (c *clientHandler) handleListSessions() error {
	sessions := c.server.manager.List()
	infos := make([]protocol.SessionInfo, len(sessions))
	for i, s := range sessions {
		infos[i] = protocol.SessionInfo{
			ID:      s.ID,
			Name:    s.Name,
			Created: s.Created.Unix(),
			Clients: s.ClientCount(),
		}
	}

	msg, err := protocol.NewMessage(protocol.MsgSessionList, protocol.SessionListPayload{Sessions: infos})
	if err != nil {
		return err
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return protocol.WriteMessage(c.conn, msg)
}

func (c *clientHandler) handleCreateSession(msg *protocol.Message) error {
	var payload protocol.CreateSessionPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return err
	}

	cols, rows := payload.Cols, payload.Rows
	if cols == 0 {
		cols = 80
	}
	if rows == 0 {
		rows = 24
	}

	sess, err := c.server.manager.Create(payload.Name, cols, rows)
	if err != nil {
		return err
	}

	log.Printf("Session %s (%s) created", sess.ID, sess.Name)

	info := protocol.SessionInfo{
		ID:      sess.ID,
		Name:    sess.Name,
		Created: sess.Created.Unix(),
		Clients: 0,
	}

	respMsg, err := protocol.NewMessage(protocol.MsgSessionList, protocol.SessionListPayload{Sessions: []protocol.SessionInfo{info}})
	if err != nil {
		return err
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return protocol.WriteMessage(c.conn, respMsg)
}

func (c *clientHandler) handleDeleteSession(msg *protocol.Message) error {
	var payload protocol.DeleteSessionPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return err
	}

	if err := c.server.manager.Delete(payload.SessionID); err != nil {
		return err
	}

	log.Printf("Session %s deleted", payload.SessionID)
	return c.sendOK()
}

func (c *clientHandler) handleAttach(msg *protocol.Message) error {
	var payload protocol.AttachPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return err
	}

	sess, ok := c.server.manager.Get(payload.SessionID)
	if !ok {
		return fmt.Errorf("session not found: %s", payload.SessionID)
	}

	c.attachMu.Lock()
	if c.attached != nil {
		c.attached.RemoveClient(c.clientID)
		if c.outputBuf != nil {
			c.outputBuf.Close()
		}
	}

	c.outputBuf = &outputBuffer{ch: c}
	sess.AddClient(c.clientID, c.outputBuf)
	c.attached = sess
	c.attachMu.Unlock()

	if payload.Cols > 0 && payload.Rows > 0 {
		sess.Resize(payload.Cols, payload.Rows)
	}

	log.Printf("Client %s attached to session %s", c.clientID, sess.ID)
	return c.sendOK()
}

func (c *clientHandler) handleDetach() error {
	c.attachMu.Lock()
	defer c.attachMu.Unlock()

	if c.attached == nil {
		return nil
	}

	c.attached.RemoveClient(c.clientID)
	if c.outputBuf != nil {
		c.outputBuf.Close()
	}
	log.Printf("Client %s detached from session %s", c.clientID, c.attached.ID)
	c.attached = nil
	c.outputBuf = nil

	return c.sendOK()
}

func (c *clientHandler) handleInput(msg *protocol.Message) error {
	c.attachMu.Lock()
	sess := c.attached
	c.attachMu.Unlock()

	if sess == nil {
		return fmt.Errorf("not attached to any session")
	}

	var payload protocol.InputPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return err
	}

	_, err := sess.Write(payload.Data)
	return err
}

func (c *clientHandler) handleResize(msg *protocol.Message) error {
	c.attachMu.Lock()
	sess := c.attached
	c.attachMu.Unlock()

	if sess == nil {
		return nil
	}

	var payload protocol.ResizePayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return err
	}

	return sess.Resize(payload.Cols, payload.Rows)
}

func (c *clientHandler) sendOK() error {
	msg, _ := protocol.NewMessage(protocol.MsgOK, nil)
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return protocol.WriteMessage(c.conn, msg)
}

func (c *clientHandler) sendError(errMsg string) error {
	msg, _ := protocol.NewMessage(protocol.MsgError, protocol.ErrorPayload{Message: errMsg})
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return protocol.WriteMessage(c.conn, msg)
}

func main() {
	configPath := flag.String("config", "", "Path to TOML configuration file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	server := NewServer(cfg.Socket)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		<-sigCh
		log.Println("Shutting down...")
		os.Remove(cfg.Socket)
		os.Exit(0)
	}()

	// Start web server if enabled
	if cfg.Web != nil && cfg.Web.Enabled {
		webServer := web.NewServer(cfg.Socket, cfg.Web.Address)
		go func() {
			if err := webServer.Start(); err != nil {
				log.Printf("Web server error: %v", err)
			}
		}()
	}

	if err := server.Start(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}
