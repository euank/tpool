package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
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
	"golang.org/x/sync/errgroup"
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

func (s *Server) Start(ctx context.Context) error {
	os.Remove(s.sockPath)

	listener, err := net.Listen("unix", s.sockPath)
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	s.listener = listener

	if err := os.Chmod(s.sockPath, 0700); err != nil {
		return fmt.Errorf("chmod socket: %w", err)
	}

	slog.Info("Daemon listening", "socket", s.sockPath)

	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return err
		}
		go s.handleConnection(conn)
	}
}

func (s *Server) Close() error {
	if s.listener != nil {
		return s.listener.Close()
	}
	return nil
}

func (s *Server) handleConnection(conn net.Conn) {
	defer conn.Close()

	clientID := uuid.New().String()[:8]
	slog.Info("Client connected", "client", clientID)
	defer slog.Info("Client disconnected", "client", clientID)

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
			slog.Error("Client error", "client", c.clientID, "error", err)
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

	slog.Info("Session created", "id", sess.ID, "name", sess.Name)

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

	slog.Info("Session deleted", "id", payload.SessionID)
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

	slog.Info("Client attached", "client", c.clientID, "session", sess.ID)
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
	slog.Info("Client detached", "client", c.clientID, "session", c.attached.ID)
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
	if err := main_(); err != nil {
		os.Exit(1)
	}
}

func main_() error {
	configPath := flag.String("config", "", "Path to TOML configuration file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		slog.Error("Failed to load config", "error", err)
		os.Exit(1)
	}

	server := NewServer(cfg.Socket)
	defer os.Remove(cfg.Socket)

	ctx, cancel := context.WithCancel(context.Background())
	g, ctx := errgroup.WithContext(ctx)

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	g.Go(func() error {
		select {
		case <-sigCh:
			slog.Info("Shutting down")
		case <-ctx.Done():
		}
		signal.Stop(sigCh)
		cancel()
		server.Close()
		return nil
	})

	if cfg.Web != nil && cfg.Web.Enabled {
		webServer := web.NewServer(cfg.Socket, cfg.Web.Address, cfg.Web.Ngrok)
		g.Go(func() error {
			if err := webServer.Start(ctx); err != nil {
				slog.Error("server error", "err", err)
			}
			return nil
		})
	}

	g.Go(func() error {
		if err := server.Start(ctx); err != nil {
			slog.Error("server error", "err", err)
		}
		return nil
	})

	return g.Wait()
}
