# tpool Development Guide

## Build & Run Commands

```bash
# Build all binaries
make build

# Run daemon (without web)
./bin/tpoold

# Run daemon with web interface
./bin/tpoold --config tpool.toml

# Run TUI client
./bin/tpool
```

## Project Structure

- `cmd/tpoold/` - Daemon that manages PTY sessions (includes optional web server)
- `cmd/tpool/` - TUI client (bubbletea)
- `internal/config/` - TOML configuration parsing
- `internal/protocol/` - Message protocol for client-daemon IPC
- `internal/session/` - PTY session management
- `internal/web/` - Web interface (xterm.js + WebSocket)

## Tech Stack

- Go 1.22+
- TUI: github.com/charmbracelet/bubbletea
- PTY: github.com/creack/pty
- WebSocket: github.com/gorilla/websocket
- Config: github.com/BurntSushi/toml
- Frontend: xterm.js (CDN)
