class TPoolApp {
    constructor() {
        this.terminal = null;
        this.fitAddon = null;
        this.ws = null;
        this.currentSession = null;
        this.resizeTimeout = null;

        this.init();
    }

    init() {
        this.bindEvents();
        this.loadSessions();
    }

    bindEvents() {
        document.getElementById('btn-new').addEventListener('click', () => this.showCreateModal());
        document.getElementById('btn-cancel').addEventListener('click', () => this.hideCreateModal());
        document.getElementById('btn-create').addEventListener('click', () => this.createSession());
        document.getElementById('btn-detach').addEventListener('click', () => this.detach());

        document.getElementById('session-name').addEventListener('keydown', (e) => {
            if (e.key === 'Enter') this.createSession();
            if (e.key === 'Escape') this.hideCreateModal();
        });

        document.getElementById('create-modal').addEventListener('click', (e) => {
            if (e.target.id === 'create-modal') this.hideCreateModal();
        });

        window.addEventListener('resize', () => this.handleResize());
    }

    async loadSessions() {
        try {
            const response = await fetch('/api/sessions');
            const sessions = await response.json();
            this.renderSessions(sessions || []);
        } catch (error) {
            console.error('Failed to load sessions:', error);
            this.renderSessions([]);
        }
    }

    renderSessions(sessions) {
        const container = document.getElementById('sessions');
        const emptyState = document.getElementById('empty-state');

        if (sessions.length === 0) {
            container.innerHTML = '';
            emptyState.style.display = 'block';
            return;
        }

        emptyState.style.display = 'none';
        
        sessions.sort((a, b) => b.created - a.created);

        container.innerHTML = sessions.map(session => {
            const created = new Date(session.created * 1000);
            const timeStr = created.toLocaleTimeString();
            const clientsBadge = session.clients > 0 
                ? `<span class="clients-badge">${session.clients} attached</span>` 
                : '';

            return `
                <div class="session-card" data-id="${session.id}">
                    <div class="session-card-info">
                        <div class="session-card-name">${this.escapeHtml(session.name)}${clientsBadge}</div>
                        <div class="session-card-meta">
                            <span>ID: ${session.id}</span>
                            <span>Created: ${timeStr}</span>
                        </div>
                    </div>
                    <div class="session-card-actions">
                        <button class="btn" onclick="app.attach('${session.id}', '${this.escapeHtml(session.name)}')">Attach</button>
                        <button class="btn btn-danger" onclick="event.stopPropagation(); app.deleteSession('${session.id}')">Delete</button>
                    </div>
                </div>
            `;
        }).join('');

        container.querySelectorAll('.session-card').forEach(card => {
            card.addEventListener('click', (e) => {
                if (e.target.tagName !== 'BUTTON') {
                    const id = card.dataset.id;
                    const name = card.querySelector('.session-card-name').textContent.split('\n')[0].trim();
                    this.attach(id, name);
                }
            });
        });
    }

    escapeHtml(text) {
        const div = document.createElement('div');
        div.textContent = text;
        return div.innerHTML;
    }

    showCreateModal() {
        document.getElementById('create-modal').style.display = 'flex';
        document.getElementById('session-name').value = '';
        document.getElementById('session-name').focus();
    }

    hideCreateModal() {
        document.getElementById('create-modal').style.display = 'none';
    }

    async createSession() {
        const name = document.getElementById('session-name').value.trim();
        
        try {
            const response = await fetch('/api/sessions/create', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ name, cols: 80, rows: 24 })
            });

            if (!response.ok) {
                throw new Error(await response.text());
            }

            this.hideCreateModal();
            this.loadSessions();
        } catch (error) {
            console.error('Failed to create session:', error);
            alert('Failed to create session: ' + error.message);
        }
    }

    async deleteSession(id) {
        if (!confirm('Delete this session?')) return;

        try {
            const response = await fetch('/api/sessions/delete', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ session_id: id })
            });

            if (!response.ok) {
                throw new Error(await response.text());
            }

            this.loadSessions();
        } catch (error) {
            console.error('Failed to delete session:', error);
            alert('Failed to delete session: ' + error.message);
        }
    }

    attach(sessionId, sessionName) {
        this.currentSession = { id: sessionId, name: sessionName };

        document.getElementById('session-list').style.display = 'none';
        document.getElementById('terminal-container').style.display = 'flex';
        document.getElementById('btn-detach').style.display = 'block';
        document.getElementById('btn-new').style.display = 'none';
        document.getElementById('session-info').innerHTML = 
            `Attached to <span class="session-name">${this.escapeHtml(sessionName)}</span> (${sessionId})`;

        this.initTerminal(sessionId);
    }

    initTerminal(sessionId) {
        if (this.terminal) {
            this.terminal.dispose();
        }

        this.terminal = new Terminal({
            cursorBlink: true,
            fontSize: 14,
            fontFamily: 'Menlo, Monaco, "Courier New", monospace',
            theme: {
                background: '#0f0f1a',
                foreground: '#eee',
                cursor: '#e94560',
                selection: 'rgba(233, 69, 96, 0.3)',
                black: '#000000',
                red: '#e94560',
                green: '#4ecca3',
                yellow: '#ffc857',
                blue: '#4d9de0',
                magenta: '#c77dff',
                cyan: '#7ec8e3',
                white: '#eee',
                brightBlack: '#666',
                brightRed: '#ff6b6b',
                brightGreen: '#7fff94',
                brightYellow: '#ffe066',
                brightBlue: '#82b1ff',
                brightMagenta: '#ea80fc',
                brightCyan: '#a5f3fc',
                brightWhite: '#fff'
            }
        });

        this.fitAddon = new FitAddon.FitAddon();
        this.terminal.loadAddon(this.fitAddon);
        this.terminal.loadAddon(new WebLinksAddon.WebLinksAddon());

        const container = document.getElementById('terminal');
        container.innerHTML = '';
        this.terminal.open(container);
        
        setTimeout(() => {
            this.fitAddon.fit();
            this.connectWebSocket(sessionId);
        }, 100);
    }

    connectWebSocket(sessionId) {
        const cols = this.terminal.cols;
        const rows = this.terminal.rows;
        const wsProtocol = window.location.protocol === 'https:' ? 'wss:' : 'ws:';
        const wsUrl = `${wsProtocol}//${window.location.host}/ws/terminal?session=${sessionId}&cols=${cols}&rows=${rows}`;

        this.ws = new WebSocket(wsUrl);
        this.ws.binaryType = 'arraybuffer';

        this.ws.onopen = () => {
            console.log('WebSocket connected');
        };

        this.ws.onmessage = (event) => {
            if (event.data instanceof ArrayBuffer) {
                this.terminal.write(new Uint8Array(event.data));
            } else {
                this.terminal.write(event.data);
            }
        };

        this.ws.onclose = () => {
            console.log('WebSocket closed');
            this.terminal.write('\r\n\x1b[33m[Session disconnected]\x1b[0m\r\n');
        };

        this.ws.onerror = (error) => {
            console.error('WebSocket error:', error);
        };

        this.terminal.onData((data) => {
            if (this.ws && this.ws.readyState === WebSocket.OPEN) {
                this.ws.send(JSON.stringify({ type: 'input', data: data }));
            }
        });
    }

    handleResize() {
        if (!this.fitAddon || !this.terminal) return;

        clearTimeout(this.resizeTimeout);
        this.resizeTimeout = setTimeout(() => {
            this.fitAddon.fit();
            
            if (this.ws && this.ws.readyState === WebSocket.OPEN) {
                this.ws.send(JSON.stringify({
                    type: 'resize',
                    cols: this.terminal.cols,
                    rows: this.terminal.rows
                }));
            }
        }, 100);
    }

    detach() {
        if (this.ws) {
            this.ws.send(JSON.stringify({ type: 'detach' }));
            this.ws.close();
            this.ws = null;
        }

        if (this.terminal) {
            this.terminal.dispose();
            this.terminal = null;
        }

        this.currentSession = null;

        document.getElementById('session-list').style.display = 'block';
        document.getElementById('terminal-container').style.display = 'none';
        document.getElementById('btn-detach').style.display = 'none';
        document.getElementById('btn-new').style.display = 'block';
        document.getElementById('session-info').innerHTML = '';

        this.loadSessions();
    }
}

const app = new TPoolApp();
