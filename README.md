# tpool - Terminal Pool

A terminal multiplexer with a web interface. Think tmux, but accessible via web browser.

Beware that this is a vibe-coded project. The code has been reviewed by a real human too.

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

Or use the included [systemd unit file](./contrib/systemd/README.md).

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

### While Attached (TUI)

- `Ctrl+B D` - Detach from session (return to TUI)

### While Attached (Web)

- Click "Detach" button in the top bar

## Configuration

### Config File (TOML)

```toml
# Socket path (optional)
socket = "/tmp/tpool.sock"

# Web interface (disabled by default)
[web]
enabled = true
address = ":8080"

# Expose via ngrok with OAuth (optional)
[web.ngrok]
url = "https://your-domain.ngrok.app"

[web.ngrok.oauth]
provider = "github"  # "github" or "google"
allowed_users = ["your-username"]  # GitHub usernames or Google emails
```

### Environment Variables

- `TPOOL_SOCKET` - Override socket path (default: `$XDG_RUNTIME_DIR/tpool-$UID.sock`)

## License

MIT
