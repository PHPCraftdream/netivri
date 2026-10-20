# netivri

`netivri` is a local HTTP/SOCKS5 proxy CLI built on Mihomo. This package
installs the prebuilt binary for your platform; it has no postinstall
scripts and downloads nothing at install time beyond npm's own registry
traffic.

## Install and run

```sh
npm i -g netivri
netivri up --foreground
```

Update: `npm update -g netivri` (or `npm i -g netivri@latest`).
Uninstall: `npm rm -g netivri`.

The binary and its Ktav native library ship in a per-platform package
(`netivri-linux-x64`, `netivri-darwin-arm64`, `netivri-win32-x64`, ...)
installed automatically as an optional dependency. Linux needs glibc 2.17+;
musl (Alpine) is not supported.

Docs: <https://github.com/PHPCraftdream/netivri> — see `docs/cli.md` for the
JSON CLI contract and `docs/npm.md` for packaging details.
