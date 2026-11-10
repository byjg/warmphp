---
sidebar_position: 6
sidebar_label: "Worker protocol"
---

# Worker protocol

Workers start as `php -d disable_functions=<list> worker.php <mode> <entry>`, where `<mode>` is how
the entry runs: `callable` for `-mode worker` and `prefork`, `script` for `-mode legacy`. The gateway gets
`<list>` once from `php worker.php probe`, a JSON object that also names what `-mode prefork` needs and
this PHP lacks (`pcntl_fork`, `pcntl_waitpid`, `posix_kill`), and echoes a line the gateway wrote
on its stdin. The gateway stops there when the command cannot run, fails, does not run
`worker.php`, or does not pass stdin through to PHP, saying which. Later, if as many workers as
`-workers` end before any request was answered, it stops too, instead of replacing them forever.
With `-mode prefork`, the master starts as
`php -d disable_functions=<list> worker.php <mode> <entry> <socket> <workers>`, and each forked
worker speaks the protocol on its connection to `<socket>` instead of stdin and stdout. With
`-project-root`, `--project-root=<dir>` comes last, and the worker sets `PROJECT_ROOT` before it
loads the entry.

A frame is a 4-byte big-endian length followed by JSON. After booting, the worker sends the frame
`{"ready":true,"pid":<pid>}`. Then, for each request, the gateway sends a frame on stdin, with a new
random boundary, followed by the raw request body, and the worker answers on stdout with the raw
body, the boundary, and a frame:

```text
gateway → worker   {"boundary":"--warmphp-<random>","id":"<request id>","server":{...CGI vars...},"body_size":<n>,"fields":[[name,value]],"files":[{field,name,type,tmp_name,error,size}]}<n body bytes>
worker  → gateway  <body bytes>--warmphp-<random>{"status":200,"headers":["Content-Type: text/html"],"metrics":{"ms":23.9,"cpu_ms":6.2,"memory_kb":2840},"exit":false}
```

Output outside a request, such as boot messages, goes to stderr, so it never mixes with a body.
Around each request the worker writes `\x01start <id> <pid>` and `\x01end <id>` to stderr, each on a
line of its own: they come through the pipe in order with PHP's own lines, which is how the gateway
ties those lines to the request.
The gateway rejects a frame whose length is absurd (any printable byte read as a length is), answers
502 and replaces the worker.

Metric units match the [log field reference](https://github.com/byjg/warmphp#logs): `ms` and `cpu_ms` are milliseconds;
`memory_kb` is KiB despite its field name. Body and frame lengths are bytes.
