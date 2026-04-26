# Orion

`Orion` is a single-binary CLI for running a built-in `frps`/`frpc` workflow, pairing clients, and exposing local edge services through `~/.orion`.

## Get Started

1. Prepare DNS and HTTP routing.

Point your wildcard service domain to your edge entrypoint, for example `*.edge.example.com`.

Cloudflare:

- create a wildcard record for `*.edge.example.com`. The record should resolve to the machine that fronts your `orion` HTTP traffic

Caddy example:

```caddyfile
{
    acme_dns cloudflare <cloudflare_api_token>
}

*.edge.example.com {
    tls {
        dns cloudflare <cloudflare_api_token>
    }

    reverse_proxy your_ip:38397
}
```

For your deployment, replace:

- `edge.example.com` with your `base_domain`.
- `frps.example.com` with the public host clients use for `orion server start --public-host ...`
- `your_ip:38397` with the private address of the machine. Port can defaults to 38397.
- `<cloudflare_api_token>` with your real Cloudflare DNS API token

2. Start the Orion server on the machine that hosts `frps`.

```bash
orion config set base_domain edge.example.com
orion server start --public-host server_public_ip # or domain that client can access
```

This writes `~/.orion/frps.toml`, starts the bundled `frps`, and prints a pairing token.

> [!NOTE]
> 1. If you want to use domain as public host for TLS or any other thing, you should set DNS `your domain` to you public server ip and points to `server_public_ip:38398` in Caddy. Strongly Recommend.
> 2. If you use ip, make sure you allowed port 38398 on the firewall.

3. Pair a client machine.

Copy the token from `orion server start`, then on the client run:

```bash
orion pair join <token>
```

`orion pair join` tests connectivity to `frps` first. It only writes local config if that preflight succeeds.

4. Register service.

**Method 1: Persistant service**:

```bash
orion up -n my_service -p 8000
```

This rewrites `~/.orion/frpc.toml`, starts or restarts the bundled `frpc`, and exposes:

- `https://my-service.edge.example.com`

**Method 2: Temporary process-bound service**

```bash
orion serve -n my_service -p 8000 -- ./your_service
```

This keeps the terminal attached to your process and removes the proxy entry again when the served process exits.

6. Check health and status.

```bash
orion list
```

`orion list` shows:

- local Orion-tracked service state
- control-plane reachability to `frps`
- public reachability to each exposed domain

## Commands

```bash
orion config set base_domain edge.example.com
orion config show
orion server start --public-host frps.example.com
orion server status
orion server stop
orion pair show
orion pair join <token>
orion up -n my_service -p 8000
orion down -n my_service
orion serve -n my_service -p 8000 -- ./your_service
orion list
```

`orion config set base_domain ...` sets the suffix used for generated public domains.

`orion config show` prints the active Orion config, config paths, and bundled binary lookup paths.

`orion server start` writes `~/.orion/frps.toml`, starts the bundled `frps`, and prints a pairing token.

`orion server status` prints the current `frps` process status and the current pairing token.

`orion server stop` stops the managed `frps` process.

`orion pair join` stores the server connection in `~/.orion/config.json`.

`orion pair show` prints the current pairing token from the server-side config.

`orion up` and `orion serve` rewrite `~/.orion/frpc.toml` and start or restart the bundled `frpc`. `orion down` removes a registered proxy entry and updates `frpc`. `orion serve` removes its proxy entry again when the served process exits.

`orion list` shows both control-plane reachability to `frps` and public reachability to each exposed service domain.

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
localPort = 8000
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
