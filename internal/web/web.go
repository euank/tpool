package web

import (
	"context"
	"crypto/tls"
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/esk/tpool/internal/config"
	"github.com/esk/tpool/internal/protocol"
	"github.com/gorilla/websocket"
	"golang.ngrok.com/ngrok/v2"
)

//go:embed static/*
var staticFiles embed.FS

var upgrader = websocket.Upgrader{
	ReadBufferSize:  1024,
	WriteBufferSize: 1024,
	CheckOrigin: func(r *http.Request) bool {
		return true
	},
}

type DaemonClient struct {
	conn    net.Conn
	writeMu sync.Mutex
}

func NewDaemonClient(sockPath string) (*DaemonClient, error) {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return nil, err
	}
	return &DaemonClient{conn: conn}, nil
}

func (c *DaemonClient) Send(msg *protocol.Message) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return protocol.WriteMessage(c.conn, msg)
}

func (c *DaemonClient) Receive() (*protocol.Message, error) {
	return protocol.ReadMessage(c.conn)
}

func (c *DaemonClient) Close() {
	c.conn.Close()
}

type Server struct {
	sockPath string
	addr     string
	ngrokCfg *config.NgrokConfig
	mux      *http.ServeMux
}

func NewServer(sockPath, addr string, ngrokCfg *config.NgrokConfig) *Server {
	s := &Server{
		sockPath: sockPath,
		addr:     addr,
		ngrokCfg: ngrokCfg,
		mux:      http.NewServeMux(),
	}

	s.mux.HandleFunc("/", s.handleIndex)
	s.mux.HandleFunc("/static/", s.handleStatic)
	s.mux.HandleFunc("/api/sessions", s.handleSessions)
	s.mux.HandleFunc("/api/sessions/create", s.handleCreateSession)
	s.mux.HandleFunc("/api/sessions/delete", s.handleDeleteSession)
	s.mux.HandleFunc("/ws/terminal", s.handleTerminalWS)

	return s
}

func (s *Server) Start(ctx context.Context) error {
	if s.ngrokCfg != nil {
		return s.startWithNgrok(ctx)
	}

	slog.Info("Web server listening", "url", "http://"+s.addr)
	return http.ListenAndServe(s.addr, s.mux)
}

func (s *Server) startWithNgrok(ctx context.Context) error {
	agentOpts := []ngrok.AgentOption{}

	authtoken := s.ngrokCfg.Authtoken
	if authtoken == "" {
		authtoken = os.Getenv("NGROK_AUTHTOKEN")
	}
	if authtoken != "" {
		agentOpts = append(agentOpts, ngrok.WithAuthtoken(authtoken))
	}

	if s.ngrokCfg.ServerAddr != "" {
		agentOpts = append(agentOpts, ngrok.WithAgentConnectURL(s.ngrokCfg.ServerAddr))
	}

	if s.ngrokCfg.InsecureSkipVerify {
		agentOpts = append(agentOpts, ngrok.WithTLSConfig(func(c *tls.Config) {
			c.InsecureSkipVerify = true
		}))
	}

	agent, err := ngrok.NewAgent(agentOpts...)
	if err != nil {
		return fmt.Errorf("ngrok agent: %w", err)
	}

	endpointOpts := []ngrok.EndpointOption{}

	if s.ngrokCfg.URL != "" {
		endpointOpts = append(endpointOpts, ngrok.WithURL(s.ngrokCfg.URL))
	}

	if s.ngrokCfg.OAuth != nil {
		policy := s.buildTrafficPolicy()
		slog.Debug("constructed ngrok policy", "policy", policy)
		endpointOpts = append(endpointOpts, ngrok.WithTrafficPolicy(policy))
	}

	listener, err := agent.Listen(ctx, endpointOpts...)
	if err != nil {
		return fmt.Errorf("ngrok listen: %w", err)
	}

	slog.Info("Web server listening", "url", listener.URL())

	return http.Serve(listener, s.mux)
}

type trafficPolicy struct {
	OnHTTPRequest []policyRule `json:"on_http_request"`
}

type policyRule struct {
	Expressions []string       `json:"expressions,omitempty"`
	Actions     []policyAction `json:"actions"`
}

type policyAction struct {
	Type   string         `json:"type"`
	Config map[string]any `json:"config,omitempty"`
}

func (s *Server) buildTrafficPolicy() string {
	if s.ngrokCfg.OAuth == nil {
		return ""
	}

	provider := s.ngrokCfg.OAuth.Provider
	if provider == "" {
		provider = "github"
	}

	policy := trafficPolicy{
		OnHTTPRequest: []policyRule{
			{
				Actions: []policyAction{
					{
						Type:   "oauth",
						Config: map[string]any{"provider": provider},
					},
				},
			},
		},
	}

	if len(s.ngrokCfg.OAuth.AllowedUsers) > 0 {
		// Use email for all providers - it's the most reliable identifier
		identityField := "actions.ngrok.oauth.identity.email"

		usersJSON, _ := json.Marshal(s.ngrokCfg.OAuth.AllowedUsers)
		celExpr := fmt.Sprintf("!(%s in %s)", identityField, string(usersJSON))

		policy.OnHTTPRequest = append(policy.OnHTTPRequest, policyRule{
			Expressions: []string{celExpr},
			Actions: []policyAction{
				{
					Type: "custom-response",
					Config: map[string]any{
						"status_code": 403,
						"content":     "Access denied. Your account (${" + identityField + "}) is not authorized.",
						"headers":     map[string]string{"content-type": "text/plain"},
					},
				},
			},
		})
	}

	data, _ := json.Marshal(policy)
	return string(data)
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	content, err := staticFiles.ReadFile("static/index.html")
	if err != nil {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "text/html")
	w.Write(content)
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path[1:]
	content, err := staticFiles.ReadFile(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}

	switch filepath.Ext(path) {
	case ".js":
		w.Header().Set("Content-Type", "application/javascript")
	case ".css":
		w.Header().Set("Content-Type", "text/css")
	}
	w.Write(content)
}

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	client, err := NewDaemonClient(s.sockPath)
	if err != nil {
		http.Error(w, "Daemon not available", http.StatusServiceUnavailable)
		return
	}
	defer client.Close()

	msg, _ := protocol.NewMessage(protocol.MsgListSessions, nil)
	if err := client.Send(msg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp, err := client.Receive()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	payload, _ := protocol.ParsePayload[protocol.SessionListPayload](resp)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(payload.Sessions)
}

func (s *Server) handleCreateSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		Name string `json:"name"`
		Cols int    `json:"cols"`
		Rows int    `json:"rows"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		req.Cols = 80
		req.Rows = 24
	}

	client, err := NewDaemonClient(s.sockPath)
	if err != nil {
		http.Error(w, "Daemon not available", http.StatusServiceUnavailable)
		return
	}
	defer client.Close()

	msg, _ := protocol.NewMessage(protocol.MsgCreateSession, protocol.CreateSessionPayload{
		Name: req.Name,
		Cols: req.Cols,
		Rows: req.Rows,
	})
	if err := client.Send(msg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp, err := client.Receive()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if resp.Type == protocol.MsgError {
		payload, _ := protocol.ParsePayload[protocol.ErrorPayload](resp)
		http.Error(w, payload.Message, http.StatusBadRequest)
		return
	}

	payload, _ := protocol.ParsePayload[protocol.SessionListPayload](resp)
	w.Header().Set("Content-Type", "application/json")
	if len(payload.Sessions) > 0 {
		json.NewEncoder(w).Encode(payload.Sessions[0])
	}
}

func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	client, err := NewDaemonClient(s.sockPath)
	if err != nil {
		http.Error(w, "Daemon not available", http.StatusServiceUnavailable)
		return
	}
	defer client.Close()

	msg, _ := protocol.NewMessage(protocol.MsgDeleteSession, protocol.DeleteSessionPayload{
		SessionID: req.SessionID,
	})
	if err := client.Send(msg); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	resp, err := client.Receive()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if resp.Type == protocol.MsgError {
		payload, _ := protocol.ParsePayload[protocol.ErrorPayload](resp)
		http.Error(w, payload.Message, http.StatusBadRequest)
		return
	}

	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleTerminalWS(w http.ResponseWriter, r *http.Request) {
	sessionID := r.URL.Query().Get("session")
	if sessionID == "" {
		http.Error(w, "session parameter required", http.StatusBadRequest)
		return
	}

	ws, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		slog.Error("WebSocket upgrade error", "error", err)
		return
	}
	defer ws.Close()

	client, err := NewDaemonClient(s.sockPath)
	if err != nil {
		ws.WriteMessage(websocket.TextMessage, []byte("Daemon not available"))
		return
	}
	defer client.Close()

	cols, rows := 80, 24
	if c := r.URL.Query().Get("cols"); c != "" {
		fmt.Sscanf(c, "%d", &cols)
	}
	if rr := r.URL.Query().Get("rows"); rr != "" {
		fmt.Sscanf(rr, "%d", &rows)
	}

	msg, _ := protocol.NewMessage(protocol.MsgAttach, protocol.AttachPayload{
		SessionID: sessionID,
		Cols:      cols,
		Rows:      rows,
	})
	if err := client.Send(msg); err != nil {
		ws.WriteMessage(websocket.TextMessage, []byte("Failed to attach"))
		return
	}

	resp, err := client.Receive()
	if err != nil {
		ws.WriteMessage(websocket.TextMessage, []byte("Failed to attach"))
		return
	}

	if resp.Type == protocol.MsgError {
		payload, _ := protocol.ParsePayload[protocol.ErrorPayload](resp)
		ws.WriteMessage(websocket.TextMessage, []byte(payload.Message))
		return
	}

	done := make(chan struct{})
	var wsMu sync.Mutex

	go func() {
		defer close(done)
		for {
			msg, err := client.Receive()
			if err != nil {
				return
			}

			switch msg.Type {
			case protocol.MsgOutput:
				payload, _ := protocol.ParsePayload[protocol.OutputPayload](msg)
				wsMu.Lock()
				ws.WriteMessage(websocket.BinaryMessage, payload.Data)
				wsMu.Unlock()
			case protocol.MsgOK:
				return
			case protocol.MsgError:
				payload, _ := protocol.ParsePayload[protocol.ErrorPayload](msg)
				wsMu.Lock()
				ws.WriteMessage(websocket.TextMessage, []byte("\r\nError: "+payload.Message+"\r\n"))
				wsMu.Unlock()
				return
			}
		}
	}()

	go func() {
		for {
			msgType, data, err := ws.ReadMessage()
			if err != nil {
				client.Send(&protocol.Message{Type: protocol.MsgDetach})
				return
			}

			switch msgType {
			case websocket.BinaryMessage, websocket.TextMessage:
				var wsMsg struct {
					Type string `json:"type"`
					Cols int    `json:"cols"`
					Rows int    `json:"rows"`
					Data string `json:"data"`
				}

				if err := json.Unmarshal(data, &wsMsg); err == nil && wsMsg.Type != "" {
					switch wsMsg.Type {
					case "resize":
						msg, _ := protocol.NewMessage(protocol.MsgResize, protocol.ResizePayload{
							Cols: wsMsg.Cols,
							Rows: wsMsg.Rows,
						})
						client.Send(msg)
					case "input":
						msg, _ := protocol.NewMessage(protocol.MsgInput, protocol.InputPayload{
							Data: []byte(wsMsg.Data),
						})
						client.Send(msg)
					case "detach":
						client.Send(&protocol.Message{Type: protocol.MsgDetach})
						return
					}
				} else {
					msg, _ := protocol.NewMessage(protocol.MsgInput, protocol.InputPayload{Data: data})
					client.Send(msg)
				}
			}
		}
	}()

	<-done
}
