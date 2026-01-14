package session

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/creack/pty"
	"github.com/google/uuid"
)

type Session struct {
	ID      string
	Name    string
	Created time.Time

	pty     *os.File
	cmd     *exec.Cmd
	clients map[string]io.Writer
	mu      sync.RWMutex
	done    chan struct{}
}

type Manager struct {
	sessions map[string]*Session
	mu       sync.RWMutex
	sockPath string
}

func NewManager(sockPath string) *Manager {
	return &Manager{
		sessions: make(map[string]*Session),
		sockPath: sockPath,
	}
}

func (m *Manager) Create(name string, cols, rows int) (*Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	id := uuid.New().String()[:8]
	if name == "" {
		name = fmt.Sprintf("session-%s", id)
	}

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}

	cmd := exec.Command(shell)
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
	})
	if err != nil {
		return nil, fmt.Errorf("start pty: %w", err)
	}

	sess := &Session{
		ID:      id,
		Name:    name,
		Created: time.Now(),
		pty:     ptmx,
		cmd:     cmd,
		clients: make(map[string]io.Writer),
		done:    make(chan struct{}),
	}

	m.sessions[id] = sess

	go sess.readLoop()
	go sess.waitLoop(m)

	return sess, nil
}

func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	sess, ok := m.sessions[id]
	return sess, ok
}

func (m *Manager) List() []*Session {
	m.mu.RLock()
	defer m.mu.RUnlock()

	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	return sessions
}

func (m *Manager) Delete(id string) error {
	m.mu.Lock()
	sess, ok := m.sessions[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("session not found: %s", id)
	}
	delete(m.sessions, id)
	m.mu.Unlock()

	sess.Close()
	return nil
}

func (m *Manager) remove(id string) {
	m.mu.Lock()
	delete(m.sessions, id)
	m.mu.Unlock()
}

func (s *Session) readLoop() {
	buf := make([]byte, 4096)
	for {
		n, err := s.pty.Read(buf)
		if err != nil {
			close(s.done)
			return
		}

		data := make([]byte, n)
		copy(data, buf[:n])

		s.mu.RLock()
		for _, w := range s.clients {
			w.Write(data)
		}
		s.mu.RUnlock()
	}
}

func (s *Session) waitLoop(m *Manager) {
	s.cmd.Wait()
	m.remove(s.ID)
}

func (s *Session) Write(p []byte) (int, error) {
	return s.pty.Write(p)
}

func (s *Session) Resize(cols, rows int) error {
	return pty.Setsize(s.pty, &pty.Winsize{
		Cols: uint16(cols),
		Rows: uint16(rows),
	})
}

func (s *Session) AddClient(id string, w io.Writer) {
	s.mu.Lock()
	s.clients[id] = w
	s.mu.Unlock()
}

func (s *Session) RemoveClient(id string) {
	s.mu.Lock()
	delete(s.clients, id)
	s.mu.Unlock()
}

func (s *Session) ClientCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.clients)
}

func (s *Session) Done() <-chan struct{} {
	return s.done
}

func (s *Session) Close() {
	s.pty.Close()
	s.cmd.Process.Kill()
}

func (s *Session) PID() int {
	if s.cmd != nil && s.cmd.Process != nil {
		return s.cmd.Process.Pid
	}
	return 0
}
