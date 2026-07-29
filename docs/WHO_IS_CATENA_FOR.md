# Who Is Catena For?

Catena is a single-node HTTP and WebSocket sidecar for an existing SQLite file.

It is for developers and operators who already have data in SQLite on one machine and need a trusted dashboard, script, service, or nearby device to access that data without writing and maintaining a custom API.

## Primary Scenario

A small computer, desktop application, lab machine, or edge device stores operational data in SQLite. Another trusted process needs to query or update that database over HTTP.

For example:

```text
Sensors or local application
            |
       existing.db
            |
          Catena
       HTTP + WebSocket
            |
 Dashboard / automation / trusted service
```

Catena keeps the original database file, adds a network boundary, serializes writes, and can notify subscribers about successful writes performed through Catena.

## Good Fit

Catena is a good fit when all of these are true:

- SQLite already runs on a single machine.
- You want to keep the real `.db` file.
- Clients are trusted and operate on a private network, VPN, or protected host.
- Raw parameterized SQL is useful to those clients.
- Running PostgreSQL or a distributed database would add unnecessary operations.
- A small binary is preferable to maintaining a custom CRUD service.

Typical environments include:

- edge and IoT gateways;
- lab and scientific equipment;
- factory or field computers;
- internal dashboards;
- desktop applications with a companion service;
- local automation and maintenance scripts;
- read-only access to small internal datasets.

## Not a Fit

Do not choose Catena when you need:

- public, untrusted SQL access;
- multi-tenant isolation;
- row-level permissions;
- high availability or replication;
- writes coordinated across multiple database nodes;
- automatic synchronization of offline clients;
- notifications for changes made directly by other SQLite connections;
- a replacement for PostgreSQL, Supabase, or a full application backend.

## Product Boundary

Catena owns the network access path, not the application domain.

It provides:

- authenticated HTTP access to parameterized SQL;
- atomic write batches;
- physical SQLite read-only mode;
- serialized writes and concurrent reads through WAL;
- bounded query responses;
- change notifications for writes made through Catena;
- inspection, backup, export, metrics, and a small admin UI.

It intentionally does not generate domain-specific REST resources, business rules, user accounts, or synchronization semantics.

## Why Not the Alternatives?

- **A custom API** gives full domain control, but requires application code, deployment, tests, and maintenance.
- **Datasette** is especially strong for exploring and publishing SQLite data; Catena focuses on trusted raw SQL access and write transactions.
- **rqlite** adds a networked, fault-tolerant database architecture; Catena stays single-node and keeps the existing SQLite file as the center of the system.
- **Postlite** exposes SQLite through the PostgreSQL wire protocol; Catena targets HTTP, scripts, dashboards, and WebSocket notifications.

## Current Product Hypothesis

> Developers operating a single-machine SQLite workload will use Catena when they need a small, protected network API for an existing database and do not want to build a custom service or operate a larger database server.

This is a hypothesis, not yet a validated market claim. New features should be accepted only when they strengthen this scenario or are supported by repeated user evidence.
