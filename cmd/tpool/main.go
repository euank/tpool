package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/esk/tpool/internal/protocol"
	"golang.org/x/term"
)

func getSocketPath() string {
	if path := os.Getenv("TPOOL_SOCKET"); path != "" {
		return path
	}
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	if runtimeDir == "" {
		runtimeDir = "/tmp"
	}
	return filepath.Join(runtimeDir, fmt.Sprintf("tpool-%d.sock", os.Getuid()))
}

type Client struct {
	conn    net.Conn
	writeMu sync.Mutex
}

func NewClient(sockPath string) (*Client, error) {
	conn, err := net.Dial("unix", sockPath)
	if err != nil {
		return nil, err
	}
	return &Client{conn: conn}, nil
}

func (c *Client) Send(msg *protocol.Message) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return protocol.WriteMessage(c.conn, msg)
}

func (c *Client) Receive() (*protocol.Message, error) {
	return protocol.ReadMessage(c.conn)
}

func (c *Client) Close() {
	c.conn.Close()
}

func (c *Client) ListSessions() ([]protocol.SessionInfo, error) {
	msg, _ := protocol.NewMessage(protocol.MsgListSessions, nil)
	if err := c.Send(msg); err != nil {
		return nil, err
	}

	resp, err := c.Receive()
	if err != nil {
		return nil, err
	}

	if resp.Type == protocol.MsgError {
		payload, _ := protocol.ParsePayload[protocol.ErrorPayload](resp)
		return nil, fmt.Errorf("%s", payload.Message)
	}

	payload, err := protocol.ParsePayload[protocol.SessionListPayload](resp)
	if err != nil {
		return nil, err
	}

	return payload.Sessions, nil
}

func (c *Client) CreateSession(name string, cols, rows int) (*protocol.SessionInfo, error) {
	msg, _ := protocol.NewMessage(protocol.MsgCreateSession, protocol.CreateSessionPayload{
		Name: name,
		Cols: cols,
		Rows: rows,
	})
	if err := c.Send(msg); err != nil {
		return nil, err
	}

	resp, err := c.Receive()
	if err != nil {
		return nil, err
	}

	if resp.Type == protocol.MsgError {
		payload, _ := protocol.ParsePayload[protocol.ErrorPayload](resp)
		return nil, fmt.Errorf("%s", payload.Message)
	}

	payload, err := protocol.ParsePayload[protocol.SessionListPayload](resp)
	if err != nil {
		return nil, err
	}

	if len(payload.Sessions) > 0 {
		return &payload.Sessions[0], nil
	}
	return nil, fmt.Errorf("no session returned")
}

func (c *Client) DeleteSession(id string) error {
	msg, _ := protocol.NewMessage(protocol.MsgDeleteSession, protocol.DeleteSessionPayload{SessionID: id})
	if err := c.Send(msg); err != nil {
		return err
	}

	resp, err := c.Receive()
	if err != nil {
		return err
	}

	if resp.Type == protocol.MsgError {
		payload, _ := protocol.ParsePayload[protocol.ErrorPayload](resp)
		return fmt.Errorf("%s", payload.Message)
	}

	return nil
}

func (c *Client) Attach(sessionID string, cols, rows int) error {
	msg, _ := protocol.NewMessage(protocol.MsgAttach, protocol.AttachPayload{
		SessionID: sessionID,
		Cols:      cols,
		Rows:      rows,
	})
	if err := c.Send(msg); err != nil {
		return err
	}

	resp, err := c.Receive()
	if err != nil {
		return err
	}

	if resp.Type == protocol.MsgError {
		payload, _ := protocol.ParsePayload[protocol.ErrorPayload](resp)
		return fmt.Errorf("%s", payload.Message)
	}

	return nil
}

func (c *Client) Detach() error {
	msg, _ := protocol.NewMessage(protocol.MsgDetach, nil)
	return c.Send(msg)
}

func (c *Client) SendInput(data []byte) error {
	msg, _ := protocol.NewMessage(protocol.MsgInput, protocol.InputPayload{Data: data})
	return c.Send(msg)
}

func (c *Client) SendResize(cols, rows int) error {
	msg, _ := protocol.NewMessage(protocol.MsgResize, protocol.ResizePayload{Cols: cols, Rows: rows})
	return c.Send(msg)
}

type viewState int

const (
	viewList viewState = iota
	viewCreate
)

type model struct {
	client     *Client
	sessions   []protocol.SessionInfo
	cursor     int
	view       viewState
	createName string
	width      int
	height     int
	err        error
	quitting   bool
	attachToID string
}

type sessionsUpdated struct {
	sessions []protocol.SessionInfo
	err      error
}

type sessionCreated struct {
	session *protocol.SessionInfo
	err     error
}

type sessionDeleted struct {
	err error
}

func initialModel(client *Client) model {
	return model{
		client: client,
		view:   viewList,
	}
}

func (m model) Init() tea.Cmd {
	return m.fetchSessions
}

func (m model) fetchSessions() tea.Msg {
	sessions, err := m.client.ListSessions()
	return sessionsUpdated{sessions: sessions, err: err}
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case sessionsUpdated:
		m.err = msg.err
		if msg.err == nil {
			m.sessions = msg.sessions
			sort.Slice(m.sessions, func(i, j int) bool {
				return m.sessions[i].Created > m.sessions[j].Created
			})
			if m.cursor >= len(m.sessions) {
				m.cursor = max(0, len(m.sessions)-1)
			}
		}
		return m, nil

	case sessionCreated:
		if msg.err != nil {
			m.err = msg.err
		} else {
			m.view = viewList
			m.createName = ""
		}
		return m, m.fetchSessions

	case sessionDeleted:
		m.err = msg.err
		return m, m.fetchSessions

	case tea.KeyMsg:
		return m.handleKey(msg)
	}

	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch m.view {
	case viewList:
		return m.handleListKey(msg)
	case viewCreate:
		return m.handleCreateKey(msg)
	}
	return m, nil
}

func (m model) handleListKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit

	case "up", "k":
		if m.cursor > 0 {
			m.cursor--
		}

	case "down", "j":
		if m.cursor < len(m.sessions)-1 {
			m.cursor++
		}

	case "enter":
		if len(m.sessions) > 0 {
			m.attachToID = m.sessions[m.cursor].ID
			return m, tea.Quit
		}

	case "c", "n":
		m.view = viewCreate
		m.createName = ""

	case "d", "x":
		if len(m.sessions) > 0 {
			return m, m.deleteSession(m.sessions[m.cursor].ID)
		}

	case "r":
		return m, m.fetchSessions
	}

	return m, nil
}

func (m model) handleCreateKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.view = viewList
		m.createName = ""

	case "enter":
		return m, m.createSession(m.createName)

	case "backspace":
		if len(m.createName) > 0 {
			m.createName = m.createName[:len(m.createName)-1]
		}

	default:
		if len(msg.String()) == 1 {
			m.createName += msg.String()
		}
	}

	return m, nil
}

func (m model) deleteSession(id string) tea.Cmd {
	return func() tea.Msg {
		err := m.client.DeleteSession(id)
		return sessionDeleted{err: err}
	}
}

func (m model) createSession(name string) tea.Cmd {
	return func() tea.Msg {
		cols, rows := 80, 24
		if w, h, err := term.GetSize(int(os.Stdin.Fd())); err == nil {
			cols, rows = w, h
		}

		session, err := m.client.CreateSession(name, cols, rows)
		return sessionCreated{session: session, err: err}
	}
}

var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("205")).
			MarginBottom(1)

	selectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("229")).
			Background(lipgloss.Color("57")).
			Padding(0, 1)

	normalStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252")).
			Padding(0, 1)

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			MarginTop(1)

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196"))

	inputStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			Padding(0, 1)
)

func (m model) View() string {
	if m.quitting {
		return ""
	}

	switch m.view {
	case viewList:
		return m.viewListScreen()
	case viewCreate:
		return m.viewCreateScreen()
	}

	return ""
}

func (m model) viewListScreen() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render("🖥  tpool - Terminal Pool"))
	b.WriteString("\n\n")

	if m.err != nil {
		b.WriteString(errorStyle.Render(fmt.Sprintf("Error: %v", m.err)))
		b.WriteString("\n\n")
	}

	if len(m.sessions) == 0 {
		b.WriteString(normalStyle.Render("No sessions. Press 'c' to create one."))
		b.WriteString("\n")
	} else {
		for i, s := range m.sessions {
			created := time.Unix(s.Created, 0).Format("15:04:05")
			clients := ""
			if s.Clients > 0 {
				clients = fmt.Sprintf(" (%d attached)", s.Clients)
			}

			line := fmt.Sprintf("%s  [%s]  %s%s", s.ID, created, s.Name, clients)

			if i == m.cursor {
				b.WriteString(selectedStyle.Render("▸ " + line))
			} else {
				b.WriteString(normalStyle.Render("  " + line))
			}
			b.WriteString("\n")
		}
	}

	b.WriteString(helpStyle.Render("\n↑/↓: navigate • enter: attach • c: create • d: delete • r: refresh • q: quit"))

	return b.String()
}

func (m model) viewCreateScreen() string {
	var b strings.Builder

	b.WriteString(titleStyle.Render("Create New Session"))
	b.WriteString("\n\n")

	b.WriteString("Session name (optional):\n")
	b.WriteString(inputStyle.Render(m.createName + "█"))
	b.WriteString("\n")

	b.WriteString(helpStyle.Render("\nenter: create • esc: cancel"))

	return b.String()
}

func runAttachedSession(client *Client, sessionID string) error {
	cols, rows := 80, 24
	if w, h, err := term.GetSize(int(os.Stdin.Fd())); err == nil {
		cols, rows = w, h
	}

	if err := client.Attach(sessionID, cols, rows); err != nil {
		return err
	}

	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		return fmt.Errorf("failed to set raw mode: %w", err)
	}
	defer term.Restore(int(os.Stdin.Fd()), oldState)

	// Clear screen and move cursor to top-left, then request shell to redraw
	os.Stdout.WriteString("\033[2J\033[H")
	// Send Ctrl+L to the session to trigger a redraw
	client.SendInput([]byte{0x0c})

	done := make(chan struct{})

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
				os.Stdout.Write(payload.Data)
			case protocol.MsgOK:
				return
			case protocol.MsgError:
				payload, _ := protocol.ParsePayload[protocol.ErrorPayload](msg)
				fmt.Fprintf(os.Stderr, "\r\nError: %s\r\n", payload.Message)
				return
			}
		}
	}()

	buf := make([]byte, 4096)
	ctrlB := false

	for {
		select {
		case <-done:
			return nil
		default:
		}

		n, err := os.Stdin.Read(buf)
		if err != nil {
			client.Detach()
			return nil
		}

		data := buf[:n]

		// Check for Ctrl+B D detach sequence
		for i := 0; i < len(data); i++ {
			if ctrlB {
				ctrlB = false
				if data[i] == 'd' || data[i] == 'D' {
					// Detach sequence found
					// Send any data before the Ctrl+B
					if i > 1 {
						client.SendInput(data[:i-1])
					}
					client.Detach()
					<-done
					return nil
				}
				// Not a detach, send the Ctrl+B and continue
				client.SendInput([]byte{0x02})
				// Send from current position
				client.SendInput(data[i:])
				data = nil
				break
			}
			if data[i] == 0x02 {
				// Ctrl+B pressed, send everything before it
				if i > 0 {
					client.SendInput(data[:i])
				}
				ctrlB = true
				data = data[i+1:]
				i = -1 // Reset loop for remaining data
			}
		}

		// Send remaining data if not consumed
		if len(data) > 0 && !ctrlB {
			client.SendInput(data)
		}
	}
}

func ensureDaemon(sockPath string) error {
	conn, err := net.Dial("unix", sockPath)
	if err == nil {
		conn.Close()
		return nil
	}
	return fmt.Errorf("daemon not running")
}

func runTUI(sockPath string) (attachTo string, err error) {
	client, err := NewClient(sockPath)
	if err != nil {
		return "", fmt.Errorf("failed to connect to daemon: %w", err)
	}
	defer client.Close()

	m := initialModel(client)
	p := tea.NewProgram(m, tea.WithAltScreen())
	finalModel, err := p.Run()
	if err != nil {
		return "", err
	}

	if fm, ok := finalModel.(model); ok && fm.attachToID != "" {
		return fm.attachToID, nil
	}

	return "", nil
}

func main() {
	sockPath := getSocketPath()

	if err := ensureDaemon(sockPath); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		fmt.Fprintf(os.Stderr, "Please start the daemon: tpoold\n")
		os.Exit(1)
	}

	for {
		attachTo, err := runTUI(sockPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		if attachTo == "" {
			break
		}

		client, err := NewClient(sockPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Failed to connect: %v\n", err)
			os.Exit(1)
		}

		if err := runAttachedSession(client, attachTo); err != nil {
			fmt.Fprintf(os.Stderr, "Session error: %v\n", err)
		}

		client.Close()
	}
}
