# tg-monitor

`tg-monitor` is a private Telegram server-monitoring system. The current foundation provides transport-independent metric and server contracts, validated environment configuration, UTC minute aggregation, and a pure-Go SQLite repository. Later HTTP, Agent, Telegram Bot, and frontend adapters build on these packages.

SQLite runs in WAL mode with foreign-key enforcement and a busy timeout. Current data, minute history, sessions, preferences, settings, and alert-delivery state are managed under `internal/storage/sqlite`.

## Development

Use the pinned Go 1.26.5 toolchain:

```bash
PATH=/tmp/go-toolchain/bin:$PATH go test ./...
PATH=/tmp/go-toolchain/bin:$PATH go vet ./...
```

The project is licensed under the MIT License. See `NOTICE` for inspiration attribution.
