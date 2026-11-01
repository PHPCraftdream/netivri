# Server protocols

Protocol documentation: [Shadowsocks](shadowsocks.md). The `server` commands manage registered inbound listeners; the concise CLI reference is [Server CLI](cli.md). The live listener and connection summary appears in `netivri status` and each `netivri status --watch` frame (see the status contract in [docs/cli.md](../cli.md#status)).

Example for a loopback-only listener:

```
netivri server add --protocol shadowsocks --account alice --port 8388 --bind 127.0.0.1 --public-host edge.example.org --param cipher=aes-128-gcm
netivri server list
netivri status
```

Keep the listener on loopback unless remote access is intended and protected appropriately. Server links contain credentials; do not paste or log them publicly.
