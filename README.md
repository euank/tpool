# tpool - Terminal Pool

A modern terminal multiplexer with TUI and web interfaces.

## Features

- **Multi-client**: Multiple clients can attach to the same session simultaneously
- **Persistent sessions**: Sessions continue running after disconnect
- **TUI-first**: Launch `tpool` for an interactive session manager
- **Web interface**: Browser-based terminal access with xterm.js

## Building

```bash
make build
```

Binaries will be in `bin/`.

## Usage

### Start the daemon

```bash
tpoold
```

Or just run `tpool` - it will auto-start the daemon.

### Launch the TUI

```bash
tpool
```

### Enable the Web Interface

Create a config file (e.g., `tpool.toml`):

```toml
[web]
enabled = true
address = ":8080"
```

Start the daemon with the config:

```bash
tpoold --config tpool.toml
```

Then open http://localhost:8080 in your browser.

### TUI Controls

| Key | Action |
|-----|--------|
| `↑`/`↓` or `j`/`k` | Navigate sessions |
| `Enter` | Attach to session |
| `c` or `n` | Create new session |
| `d` or `x` | Delete session |
| `r` | Refresh list |
| `q` | Quit |

### While Attached (TUI)

- `Ctrl+B D` - Detach from session (return to TUI)

### While Attached (Web)

- Click "Detach" button in the top bar

## Architecture

```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│  TUI Client │     │  Web Client │     │  TUI Client │
└──────┬──────┘     └──────┬──────┘     └──────┬──────┘
       │                   │                   │
       │ Unix Socket       │ WebSocket         │
       └───────────┬───────┴───────────────────┘
                   │
           ┌───────┴───────┐
           │    tpoold     │  (long-lived daemon)
           └───────┬───────┘
                   │
      ┌────────────┼────────────┐
      │            │            │
   ┌──┴──┐     ┌──┴──┐     ┌──┴──┐
   │ PTY │     │ PTY │     │ PTY │
   └─────┘     └─────┘     └─────┘
```

## Configuration

### Config File (TOML)

```toml
# Socket path (optional)
socket = "/tmp/tpool.sock"

# Web interface (disabled by default)
[web]
enabled = true
address = ":8080"
```

### Environment Variables

- `TPOOL_SOCKET` - Override socket path (default: `$XDG_RUNTIME_DIR/tpool-$UID.sock`)

## License

MIT
