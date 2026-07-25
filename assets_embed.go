package main

const openAPISpec = `{
  "openapi": "3.0.3",
  "info": {
    "title": "Catena API",
    "version": "0.3.0",
    "description": "SQLite over HTTP and WebSocket in a single Go binary."
  },
  "paths": {
    "/health": {
      "get": {
        "summary": "Health check",
        "responses": { "200": { "description": "Server is healthy" } }
      }
    },
    "/query": {
      "post": {
        "summary": "Execute one SQL statement",
        "security": [{ "ApiKeyAuth": [] }],
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
    "/export": {
      "get": {
        "summary": "Download a checkpointed SQLite database copy",
        "security": [{ "ApiKeyAuth": [] }],
        "responses": {
          "200": { "description": "SQLite database file" },
          "500": { "description": "Export failed" }
        }
      }
    },
    "/backup": {
      "post": {
        "summary": "Create a checkpointed database backup on the server",
        "security": [{ "ApiKeyAuth": [] }],
        "responses": {
          "200": { "description": "Backup created" },
          "500": { "description": "Backup failed" }
        }
      }
    },
    "/metrics": {
      "get": {
        "summary": "Return process-local JSON metrics",
        "security": [{ "ApiKeyAuth": [] }],
        "responses": { "200": { "description": "Metrics snapshot" } }
      }
    },
    "/ws": {
      "get": {
        "summary": "Subscribe to table update events over WebSocket",
        "security": [{ "ApiKeyAuth": [] }],
        "responses": { "101": { "description": "WebSocket upgrade" } }
      }
    }
  },
  "components": {
    "securitySchemes": {
      "ApiKeyAuth": { "type": "http", "scheme": "bearer" }
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
    * { box-sizing: border-box; }
    body { margin: 0; background: #f5f6f4; color: #1f2933; }
    header { display: flex; align-items: center; justify-content: space-between; gap: 16px; padding: 14px 18px; border-bottom: 1px solid #d9ded7; background: #fff; }
    h1 { font-size: 18px; margin: 0; font-weight: 750; }
    main { display: grid; grid-template-columns: minmax(280px, 360px) 1fr; min-height: calc(100vh - 55px); }
    aside, section { padding: 16px; }
    aside { display: grid; grid-template-rows: auto auto auto 1fr; gap: 14px; border-right: 1px solid #d9ded7; background: #fbfbfa; }
    section { display: grid; grid-template-rows: auto auto 1fr; gap: 14px; min-width: 0; }
    label { display: block; font-size: 12px; font-weight: 750; color: #53606b; margin-bottom: 6px; }
    input, textarea { width: 100%; border: 1px solid #c9d0c8; border-radius: 6px; background: #fff; color: #1f2933; padding: 10px; font: inherit; }
    textarea { min-height: 230px; resize: vertical; font-family: "Cascadia Code", "SFMono-Regular", Consolas, monospace; font-size: 13px; line-height: 1.5; }
    button { border: 1px solid #0f766e; background: #0f766e; color: #fff; border-radius: 6px; padding: 9px 12px; font-weight: 750; cursor: pointer; white-space: nowrap; }
    button.secondary { background: #fff; color: #0f766e; }
    button.neutral { border-color: #9aa4ae; background: #fff; color: #293642; }
    .toolbar { display: flex; flex-wrap: wrap; gap: 8px; align-items: center; }
    .panel { min-width: 0; }
    .status { font-size: 12px; color: #53606b; }
    .status strong { color: #0f766e; }
    pre, .output { margin: 0; overflow: auto; min-height: 190px; padding: 12px; border: 1px solid #d8ddd6; border-radius: 6px; background: #111827; color: #e5e7eb; font-size: 13px; line-height: 1.45; tab-size: 2; }
    #events { height: 100%; min-height: 260px; white-space: pre-wrap; overflow-wrap: anywhere; }
    #result { min-height: 360px; }
    table { width: 100%; border-collapse: collapse; color: #e5e7eb; }
    th, td { border-bottom: 1px solid #273244; padding: 7px 8px; text-align: left; vertical-align: top; }
    th { color: #a7f3d0; font-weight: 750; position: sticky; top: 0; background: #111827; }
    td { max-width: 360px; overflow-wrap: anywhere; }
    .muted { color: #9aa4ae; }
    .field-grid { display: grid; gap: 12px; }
    @media (max-width: 820px) {
      main { grid-template-columns: 1fr; }
      aside { border-right: 0; border-bottom: 1px solid #d9ded7; }
      #events { height: 240px; }
    }
  </style>
</head>
<body>
  <header>
    <h1>Catena Admin</h1>
    <span class="status" id="health">Checking...</span>
  </header>
  <main>
    <aside>
      <div class="field-grid">
        <div>
          <label for="apiKey">API key</label>
          <input id="apiKey" type="password" autocomplete="off" placeholder="Optional for local dev">
        </div>
        <div>
          <label for="table">Realtime table</label>
          <input id="table" value="*" autocomplete="off">
        </div>
      </div>
      <div class="toolbar">
        <button id="connect">Connect WS</button>
        <button class="secondary" id="disconnect">Disconnect</button>
        <button class="neutral" id="clearEvents">Clear</button>
      </div>
      <div class="toolbar">
        <button class="secondary" id="exportDb">Export DB</button>
        <button class="secondary" id="backupDb">Backup DB</button>
        <button class="neutral" id="metrics">Metrics</button>
      </div>
      <pre id="events">No events yet.</pre>
    </aside>
    <section>
      <div class="panel">
        <label for="sql">SQL</label>
        <textarea id="sql">SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name;</textarea>
      </div>
      <div class="toolbar">
        <button id="run">Run Query</button>
        <button class="secondary" id="sample">Insert Sample</button>
        <button class="neutral" id="tables">List Tables</button>
        <button class="neutral" id="clearResult">Clear Result</button>
      </div>
      <div id="result" class="output">Run a query to see results.</div>
    </section>
  </main>
  <script>
    const apiKey = document.getElementById('apiKey');
    const result = document.getElementById('result');
    const events = document.getElementById('events');
    const sql = document.getElementById('sql');
    const health = document.getElementById('health');
    let ws;

    function headers(json = true) {
      const h = {};
      if (json) h['Content-Type'] = 'application/json';
      if (apiKey.value) h.Authorization = 'Bearer ' + apiKey.value;
      return h;
    }

    function escapeHtml(value) {
      return String(value)
        .replaceAll('&', '&amp;')
        .replaceAll('<', '&lt;')
        .replaceAll('>', '&gt;')
        .replaceAll('"', '&quot;')
        .replaceAll("'", '&#039;');
    }

    function show(target, payload) {
      target.textContent = typeof payload === 'string' ? payload : JSON.stringify(payload, null, 2);
    }

    function showResult(payload) {
      if (payload && Array.isArray(payload.columns) && Array.isArray(payload.rows)) {
        const head = payload.columns.map(col => '<th>' + escapeHtml(col) + '</th>').join('');
        const body = payload.rows.map(row => '<tr>' + row.map(cell => '<td>' + escapeHtml(cell ?? '') + '</td>').join('') + '</tr>').join('');
        const empty = payload.rows.length === 0 ? '<p class="muted">No rows returned.</p>' : '';
        result.innerHTML = '<table><thead><tr>' + head + '</tr></thead><tbody>' + body + '</tbody></table>' + empty;
        return;
      }
      show(result, payload);
    }

    function appendEvent(text) {
      if (events.textContent === 'No events yet.') events.textContent = '';
      events.textContent += text + '\n';
      events.scrollTop = events.scrollHeight;
    }

    async function runQuery(statement, params = []) {
      const res = await fetch('/query', { method: 'POST', headers: headers(), body: JSON.stringify({ sql: statement, params }) });
      const data = await res.json();
      showResult(data);
      return data;
    }

    document.getElementById('run').onclick = () => runQuery(sql.value);
    document.getElementById('tables').onclick = () => {
      sql.value = "SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name;";
      runQuery(sql.value);
    };
    document.getElementById('sample').onclick = async () => {
      await runQuery('CREATE TABLE IF NOT EXISTS catena_samples (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT, created_at TEXT);');
      await runQuery('INSERT INTO catena_samples (name, created_at) VALUES (?, datetime("now"));', ['sample']);
      sql.value = 'SELECT * FROM catena_samples ORDER BY id DESC LIMIT 10;';
      await runQuery(sql.value);
    };
    document.getElementById('clearResult').onclick = () => show(result, 'Run a query to see results.');
    document.getElementById('clearEvents').onclick = () => show(events, 'No events yet.');
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
      ws.onerror = () => appendEvent('[error]');
    };
    document.getElementById('disconnect').onclick = () => { if (ws) ws.close(); };
    document.getElementById('metrics').onclick = async () => {
      const res = await fetch('/metrics', { headers: headers(false) });
      show(result, await res.json());
    };
    document.getElementById('backupDb').onclick = async () => {
      const res = await fetch('/backup', { method: 'POST', headers: headers(false) });
      show(result, await res.json());
    };
    document.getElementById('exportDb').onclick = async () => {
      const res = await fetch('/export', { headers: headers(false) });
      if (!res.ok) {
        show(result, await res.json());
        return;
      }
      const blob = await res.blob();
      const url = URL.createObjectURL(blob);
      const a = document.createElement('a');
      a.href = url;
      a.download = 'catena-export.db';
      a.click();
      URL.revokeObjectURL(url);
    };
    fetch('/health')
      .then(r => r.json())
      .then(d => health.innerHTML = '<strong>' + d.status + '</strong>')
      .catch(() => health.textContent = 'offline');
  </script>
</body>
</html>`
