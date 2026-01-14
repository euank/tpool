# systemd User Service

## Installation

1. Copy the service file to your user systemd directory:

```bash
mkdir -p ~/.config/systemd/user
cp contrib/systemd/tpoold.service ~/.config/systemd/user/
```

2. (Optional) Create a config file:

```bash
mkdir -p ~/.config/tpool
cp tpool.toml.example ~/.config/tpool/tpool.toml
# Edit as needed
```

3. If using a config file, edit the service to use it:

```bash
# In ~/.config/systemd/user/tpoold.service, uncomment:
# ExecStart=%h/.local/bin/tpoold --config %h/.config/tpool/tpool.toml
```

4. Reload systemd and enable the service:

```bash
systemctl --user daemon-reload
systemctl --user enable --now tpoold
```

## Auto-start on Login

The service is configured with `WantedBy=default.target`, so it will start automatically when you log in after enabling it.

To also start user services at boot (without logging in), enable lingering:

```bash
loginctl enable-linger $USER
```
