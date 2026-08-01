# Named commands and receipts

Named-command mode is an opt-in integration surface for Catena Node. Existing
raw SQL users are unaffected.

```bash
catena serve \
  --db app.db \
  --commands-file commands.yaml \
  --command-auth-file command-auth.yaml \
  --disable-raw-sql
```

The command file is strict trusted YAML. Input fields support string, integer,
number, and boolean types plus required/minimum/maximum constraints. Unknown
definition and request fields fail closed. SQL identifiers remain literal
trusted configuration and every caller value is passed as a named SQLite
parameter.

The auth file maps token environment variables to actor IDs and permissions:

```yaml
version: 1
tokens:
  mobile_app:
    token_env: CATENA_NODE_MOBILE_TOKEN
    actor_id: mobile-app
    permissions: [sales.create, receipts.read]
```

Every configured token environment variable must exist and be non-empty.
Bearer tokens are compared using constant-time SHA-256 digest comparison.

For a new `(command_name,idempotency_key)`, one transaction:

1. inserts the command receipt;
2. executes business write statements;
3. snapshots the bounded result;
4. inserts generation-zero Anetac events;
5. links events to the receipt;
6. commits all records together.

The request fingerprint includes command name, definition version,
authenticated actor scope, and canonical typed JSON. Same-key/same-fingerprint
retries return the stored result without executing SQL or creating events.
Same-key/different-fingerprint requests return `409 idempotency_conflict`.

Receipt states are derived from the receipt/link/outbox source tables. At-least-
once delivery can duplicate an HTTP request after crash ambiguity; the receiver
must deduplicate Anetac's stable delivery ID.
