# netivri

`netivri` is a command-line proxy service built on
[Mihomo](https://github.com/MetaCubeX/mihomo). It takes a provider
subscription link, keeps a local proxy running on one port (SOCKS5, SOCKS4 and
HTTP), and routes each login through the provider and exit country you choose.

This package installs the prebuilt binary for your platform. It has no
postinstall scripts and downloads nothing at install time beyond npm's own
registry traffic.

## Install

```sh
npm i -g @phpcraftdream/netivri
```

Update: `npm update -g @phpcraftdream/netivri` (or `npm i -g @phpcraftdream/netivri@latest`).
Uninstall: `npm rm -g @phpcraftdream/netivri`.

## Quick start

```sh
# 1. Add a subscription (the URL is a credential; keep it private)
netivri sub add https://provider.example/sub/{TOKEN} --name main

# 2. Create a login that exits through Germany
netivri account add alice --provider main --country DE

# 3. Start the background angel
netivri up --port 7890
netivri status
```

The first client that connects as `alice` sets the password. Then point a
browser or a tool at `127.0.0.1:7890`:

```sh
curl -x socks5h://alice:choose-a-password@127.0.0.1:7890 https://example.org
```

`netivri help` lists every command; `netivri help config`, `netivri help routing`
and `netivri help output` explain the configuration file, routing and the
fields each command prints. `netivri doctor` checks the whole setup in one
pass, and `netivri status --watch` shows who is active right now. Results are
written as Ktav by default; add `--json` (or set `NETIVRI_OUTPUT=json`) for
JSON.

## Platforms

The binary ships in a per-platform package (`@phpcraftdream/netivri-linux-x64`,
`@phpcraftdream/netivri-darwin-arm64`, `@phpcraftdream/netivri-w32-x64`, ...) installed automatically as
an optional dependency: Linux, macOS and Windows on x64 and arm64. The Linux
binary is static, so one package serves glibc and musl (Alpine) distributions.

## Links

- Documentation: <https://github.com/PHPCraftdream/netivri>
- CLI contract, exit and error codes: <https://github.com/PHPCraftdream/netivri/blob/main/docs/cli.md>
- Configuration file: <https://github.com/PHPCraftdream/netivri/blob/main/docs/config.md>
- Packaging details: <https://github.com/PHPCraftdream/netivri/blob/main/docs/npm.md>
- License: GPL-3.0-only
