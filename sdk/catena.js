export class CatenaClient {
  constructor(baseURL, options = {}) {
    this.baseURL = baseURL.replace(/\/$/, "");
    this.apiKey = options.apiKey || "";
  }

  headers() {
    const headers = { "Content-Type": "application/json" };
    if (this.apiKey) {
      headers.Authorization = `Bearer ${this.apiKey}`;
    }
    return headers;
  }

  async query(sql, params = []) {
    const response = await fetch(`${this.baseURL}/query`, {
      method: "POST",
      headers: this.headers(),
      body: JSON.stringify({ sql, params }),
    });
    const payload = await response.json();
    if (!response.ok) {
      throw new Error(payload.message || payload.error || "Catena query failed");
    }
    return payload;
  }

  async transaction(statements) {
    const response = await fetch(`${this.baseURL}/transaction`, {
      method: "POST",
      headers: this.headers(),
      body: JSON.stringify({ statements }),
    });
    const payload = await response.json();
    if (!response.ok) {
      throw new Error(payload.message || "Catena transaction failed");
    }
    return payload.results;
  }

  async backup() {
    const response = await fetch(`${this.baseURL}/backup`, {
      method: "POST",
      headers: this.headers(),
    });
    const payload = await response.json();
    if (!response.ok) {
      throw new Error(payload.message || "Catena backup failed");
    }
    return payload;
  }

  async metrics() {
    const response = await fetch(`${this.baseURL}/metrics`, {
      headers: this.headers(),
    });
    const payload = await response.json();
    if (!response.ok) {
      throw new Error(payload.message || "Catena metrics request failed");
    }
    return payload;
  }

  async export() {
    const response = await fetch(`${this.baseURL}/export`, {
      headers: this.headers(),
    });
    if (!response.ok) {
      const payload = await response.json();
      throw new Error(payload.message || "Catena export failed");
    }
    return response.blob();
  }

  subscribe(table, onEvent) {
    const url = new URL(this.baseURL);
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    url.pathname = "/ws";
    if (this.apiKey) {
      url.searchParams.set("token", this.apiKey);
    }

    const socket = new WebSocket(url.toString());
    socket.addEventListener("open", () => {
      socket.send(JSON.stringify({ type: "subscribe", table }));
    });
    socket.addEventListener("message", (event) => {
      onEvent(JSON.parse(event.data));
    });
    return socket;
  }
}
