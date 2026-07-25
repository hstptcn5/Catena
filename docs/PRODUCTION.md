# Production Deployment Guide

Catena v0.3.0 is a developer MVP and local production candidate. It can be useful in controlled environments, internal networks, edge devices, and small self-hosted deployments, but it exposes SQL by design. Treat it as an internal service unless you have a clear security boundary.

## Deployment Principles

- Do not expose Catena directly to the public internet without a reverse proxy and TLS.
- Always use a strong API key.
- Set a strict `--cors-origin` for browser clients.
- Use `--readonly` when serving public or semi-public data.
- Enable rate limiting for shared deployments.
- Back up the database regularly.
- Monitor `/metrics`.
- Keep the SQLite file on reliable persistent storage.

## Recommended Command

For a private/internal deployment:

```bash
catena serve \
  --db /var/lib/catena/app.db \
  --host 127.0.0.1 \
  --port 8080 \
  --api-key "$CATENA_API_KEY" \
  --cors-origin "https://your-app.example.com" \
  --rate-limit 120 \
  --backup-dir /var/lib/catena/backups
```

Binding to `127.0.0.1` keeps Catena private to the host. Put a reverse proxy in front of it for TLS and external access.

## Reverse Proxy

Example Nginx server block:

```nginx
server {
    listen 443 ssl http2;
    server_name catena.example.com;

    ssl_certificate /etc/letsencrypt/live/catena.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/catena.example.com/privkey.pem;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }

    location /ws {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Upgrade $http_upgrade;
        proxy_set_header Connection "upgrade";
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

## systemd

Create a service user and directories:

```bash
sudo useradd --system --home /var/lib/catena --shell /usr/sbin/nologin catena
sudo mkdir -p /var/lib/catena/backups
sudo chown -R catena:catena /var/lib/catena
```

Install the binary:

```bash
sudo install -m 0755 catena /usr/local/bin/catena
```

Create `/etc/catena/catena.env`:

```bash
CATENA_API_KEY=change-this-long-random-secret
```

Create `/etc/systemd/system/catena.service`:

```ini
[Unit]
Description=Catena SQLite over HTTP server
After=network.target

[Service]
User=catena
Group=catena
EnvironmentFile=/etc/catena/catena.env
ExecStart=/usr/local/bin/catena serve --db /var/lib/catena/app.db --host 127.0.0.1 --port 8080 --api-key ${CATENA_API_KEY} --backup-dir /var/lib/catena/backups --rate-limit 120
Restart=on-failure
RestartSec=3
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ReadWritePaths=/var/lib/catena

[Install]
WantedBy=multi-user.target
```

Start it:

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now catena
sudo systemctl status catena
```

## Docker Compose

Example production-oriented Compose file:

```yaml
services:
  catena:
    image: catena:latest
    command:
      - serve
      - --db
      - /app/data/app.db
      - --host
      - 0.0.0.0
      - --port
      - "8080"
      - --api-key
      - ${CATENA_API_KEY}
      - --cors-origin
      - https://your-app.example.com
      - --rate-limit
      - "120"
      - --backup-dir
      - /app/data/backups
    ports:
      - "127.0.0.1:8080:8080"
    volumes:
      - ./data:/app/data
    restart: unless-stopped
```

Use a reverse proxy such as Nginx, Caddy, Traefik, or a platform load balancer for TLS.

## Read-Only Public Data

For public datasets, prefer read-only mode:

```bash
catena serve \
  --db public.db \
  --readonly \
  --host 127.0.0.1 \
  --port 8080 \
  --api-key "$CATENA_API_KEY" \
  --cors-origin "https://your-site.example.com"
```

`--readonly` rejects write statements, including schema changes.

## Backups

Create an on-demand backup:

```bash
curl -X POST https://catena.example.com/backup \
  -H "Authorization: Bearer $CATENA_API_KEY"
```

Download an export:

```bash
curl -L https://catena.example.com/export \
  -H "Authorization: Bearer $CATENA_API_KEY" \
  -o catena-export.db
```

Recommended backup practice:

- Store backups outside the application directory.
- Copy backups to another machine or object storage.
- Test restoring backups.
- Define retention, for example hourly for 24 hours and daily for 14 days.

Catena does not currently delete old backups automatically.

## Observability

Read metrics:

```bash
curl https://catena.example.com/metrics \
  -H "Authorization: Bearer $CATENA_API_KEY"
```

Current metrics are process-local JSON counters. They reset when Catena restarts.

Watch:

- `http_error_total`
- `query_total`
- `write_query_total`
- `transaction_total`
- `websocket_clients`
- `backup_total`
- `export_total`
- `last_query_duration_ms`

## Security Checklist

- Strong API key configured.
- Catena bound to `127.0.0.1` behind a proxy, or limited by firewall.
- TLS enabled at the proxy.
- Strict `--cors-origin`.
- Rate limit enabled.
- Read-only mode enabled for public datasets.
- Backups configured and tested.
- Logs monitored.
- Raw SQL access limited to trusted clients.

## Known Limits

- No built-in TLS server.
- No row-level permissions.
- No separate read/write/admin keys yet.
- No audit log yet.
- No built-in backup retention policy yet.
- Realtime table detection handles common SQL shapes, not every advanced SQL form.

See the roadmap in `README.md` for planned improvements.
