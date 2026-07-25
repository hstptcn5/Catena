package main

const openAPISpec = `{
  "openapi": "3.0.3",
  "info": {
    "title": "Catena API",
    "version": "0.2.0",
    "description": "SQLite over HTTP and WebSocket in a single Go binary."
  },
  "paths": {
    "/health": {
      "get": {
        "summary": "Health check",
        "responses": {
          "200": {
            "description": "Server is healthy"
          }
        }
      }
    },
    "/query": {
      "post": {
        "summary": "Execute one SQL statement",
        "security": [{ "ApiKeyAuth": [] }],
        "requestBody": {
          "required": true,
          "content": {
            "application/json": {
              "schema": {
                "type": "object",
                "required": ["sql"],
                "properties": {
                  "sql": { "type": "string" },
                  "params": {
                    "type": "array",
                    "items": {}
                  }
                }
              }
            }
          }
        },
        "responses": {
          "200": { "description": "Query result" },
          "400": { "description": "Invalid request" },
          "401": { "description": "Missing or invalid API key" },
          "403": { "description": "Write rejected by read-only mode" }
        }
      }
    },
    "/transaction": {
      "post": {
        "summary": "Execute multiple write statements atomically",
        "security": [{ "ApiKeyAuth": [] }],
        "responses": {
          "200": { "description": "Transaction committed" },
          "400": { "description": "Invalid request" },
          "500": { "description": "Transaction rolled back" }
        }
      }
    },
    "/ws": {
      "get": {
        "summary": "Subscribe to table update events over WebSocket",
        "security": [{ "ApiKeyAuth": [] }],
        "responses": {
          "101": { "description": "WebSocket upgrade" }
        }
      }
    }
  },
  "components": {
    "securitySchemes": {
      "ApiKeyAuth": {
        "type": "http",
        "scheme": "bearer"
      }
    }
  }
}`

const adminHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Catena Admin</title>
  <style>
    :root { color-scheme: light; font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif; }
    body { margin: 0; background: #f7f7f5; color: #1f2933; }
    header { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 14px 20px; border-bottom: 1px solid #d8ddd6; background: #ffffff; }
    h1 { font-size: 18px; margin: 0; font-weight: 700; }
    main { display: grid; grid-template-columns: 320px 1fr; min-height: calc(100vh - 57px); }
    aside { border-right: 1px solid #d8ddd6; padding: 16px; background: #fbfbfa; }
    section { padding: 16px; }
    label { display: block; font-size: 12px; font-weight: 700; color: #53606b; margin-bottom: 6px; }
    input, textarea { width: 100%; box-sizing: border-box; border: 1px solid #c9d0c8; border-radius: 6px; background: #fff; color: #1f2933; padding: 10px; font: inherit; }
    textarea { min-height: 260px; resize: vertical; font-family: "Cascadia Code", "SFMono-Regular", Consolas, monospace; font-size: 13px; line-height: 1.5; }
    button { border: 1px solid #0f766e; background: #0f766e; color: #fff; border-radius: 6px; padding: 9px 12px; font-weight: 700; cursor: pointer; }
    button.secondary { background: #fff; color: #0f766e; }
    .stack { display: grid; gap: 12px; }
    .toolbar { display: flex; gap: 8px; align-items: center; margin-top: 12px; }
    pre { overflow: auto; min-height: 220px; padding: 12px; border: 1px solid #d8ddd6; border-radius: 6px; background: #111827; color: #e5e7eb; font-size: 13px; }
    .status { font-size: 12px; color: #53606b; }
    @media (max-width: 760px) { main { grid-template-columns: 1fr; } aside { border-right: 0; border-bottom: 1px solid #d8ddd6; } }
  </style>
</head>
<body>
  <header>
    <h1>Catena Admin</h1>
    <span class="status" id="health">Checking...</span>
  </header>
  <main>
    <aside class="stack">
      <div>
        <label for="apiKey">API key</label>
        <input id="apiKey" type="password" autocomplete="off" placeholder="Optional for local dev">
      </div>
      <div>
        <label for="table">Realtime table</label>
        <input id="table" value="*" autocomplete="off">
      </div>
      <div class="toolbar">
        <button id="connect">Connect WS</button>
        <button class="secondary" id="disconnect">Disconnect</button>
      </div>
      <pre id="events">No events yet.</pre>
    </aside>
    <section class="stack">
      <div>
        <label for="sql">SQL</label>
        <textarea id="sql">SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name;</textarea>
      </div>
      <div class="toolbar">
        <button id="run">Run Query</button>
        <button class="secondary" id="sample">Insert Sample</button>
      </div>
      <pre id="result">Run a query to see results.</pre>
    </section>
  </main>
  <script>
    const apiKey = document.getElementById('apiKey');
    const result = document.getElementById('result');
    const events = document.getElementById('events');
    const sql = document.getElementById('sql');
    let ws;

    function headers() {
      const h = { 'Content-Type': 'application/json' };
      if (apiKey.value) h.Authorization = 'Bearer ' + apiKey.value;
      return h;
    }
    function appendEvent(text) {
      if (events.textContent === 'No events yet.') events.textContent = '';
      events.textContent += text + '\n';
      events.scrollTop = events.scrollHeight;
    }
    async function runQuery(statement, params = []) {
      const res = await fetch('/query', { method: 'POST', headers: headers(), body: JSON.stringify({ sql: statement, params }) });
      const data = await res.json();
      result.textContent = JSON.stringify(data, null, 2);
    }
    document.getElementById('run').onclick = () => runQuery(sql.value);
    document.getElementById('sample').onclick = async () => {
      await runQuery('CREATE TABLE IF NOT EXISTS catena_samples (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, created_at TEXT);');
      await runQuery('INSERT INTO catena_samples (name, created_at) VALUES (?, datetime("now"));', ['sample']);
      sql.value = 'SELECT * FROM catena_samples ORDER BY id DESC LIMIT 10;';
      await runQuery(sql.value);
    };
    document.getElementById('connect').onclick = () => {
      if (ws) ws.close();
      const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
      const suffix = apiKey.value ? '?token=' + encodeURIComponent(apiKey.value) : '';
      ws = new WebSocket(proto + '//' + location.host + '/ws' + suffix);
      ws.onopen = () => {
        appendEvent('[open]');
        ws.send(JSON.stringify({ type: 'subscribe', table: document.getElementById('table').value || '*' }));
      };
      ws.onmessage = e => appendEvent(e.data);
      ws.onclose = () => appendEvent('[closed]');
    };
    document.getElementById('disconnect').onclick = () => { if (ws) ws.close(); };
    fetch('/health').then(r => r.json()).then(d => document.getElementById('health').textContent = d.status).catch(() => document.getElementById('health').textContent = 'offline');
  </script>
</body>
</html>`
