# Orion

`Orion` is a single-binary CLI for running a built-in `frps`/`frpc` workflow, pairing clients, and exposing local edge services through `~/.orion`.

## Commands

```bash
orion config set base_domain edge.example.com
orion server start --public-host frps.example.com # or ip
orion pair show
orion pair join <token>
orion up -n my_service -p 38399
orion serve -n my_service -p 38399 -- ./your_service
orion list
```

`orion server start` writes `~/.orion/frps.toml`, starts the bundled `frps`, and prints a pairing token.

`orion pair join` stores the server connection in `~/.orion/config.json`.

`orion up` and `orion serve` rewrite `~/.orion/frpc.toml` and start or restart the bundled `frpc`.

The generated client config looks like:

```toml
serverAddr = "frps.example.com"
serverPort = 38398
loginFailExit = false
auth.method = "token"
auth.token = "..."

[[proxies]]
name = "my_service"
type = "http"
localIP = "127.0.0.1"
localPort = 38399
customDomains = ["my_service.edge.example.com"]
```

`frpc` and `frps` are expected to live with the packaged project, next to the `orion` binary or under `bin/`.

GitHub tag releases bundle `orion`, `frpc`, and `frps` together and include third-party license notices for `frp`.

## Build

```bash
make build
make build-all
```

Artifacts are written to `dist/`.
