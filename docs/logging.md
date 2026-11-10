---
sidebar_position: 5
sidebar_label: "Log shipping"
---

# Sending logs to a backend

The gateway keeps no log file: it writes to stderr, and these are the ways to take the logs from there.

**[Fluent Bit](https://fluentbit.io/), through a pipe.** If it exits, the gateway keeps serving, but
the logs stop until both are restarted:

```bash
./warmphp app.php 2>&1 >/dev/null | fluent-bit -i stdin -o stdout
```

**Docker, with its `fluentd` log driver.** It sends the container's stream to Fluent Bit, wrapping
each record in a `log` field. With `fluentd-async=true`, the container starts even when Fluent Bit
is down:

```bash
docker run --log-driver fluentd --log-opt fluentd-address=127.0.0.1:24224 \
  --log-opt fluentd-async=true --log-opt tag=warmphp ...
```

Fluent Bit opens the `log` field with a parser that knows the gateway's time format. In a file, here
`warmphp.conf`:

```ini
[PARSER]
    Name        warmphp
    Format      json
    Time_Key    time
    Time_Format %Y-%m-%dT%H:%M:%S.%L%z
```

```bash
fluent-bit -R warmphp.conf -i forward -p port=24224 \
  -F parser -m '*' -p key_name=log -p parser=warmphp -o stdout -m '*'
```

Replace `-o stdout` with the output of your backend.

**A file, read by the OpenTelemetry Collector.** Redirect stderr to it, and rotate it with `logrotate`
and `copytruncate`, as nothing else does:

```bash
./warmphp app.php 2>> /logs/gateway.log
```

The [OpenTelemetry Collector](https://opentelemetry.io/docs/collector/) reads the records as logs.
This configuration of its contrib distribution (tested on 0.164) sets the time, the severity, the body
and the trace id. `include` is the path as the Collector sees it:

```yaml
receivers:
  file_log:
    include: [/logs/gateway.log]
    operators:
      - type: json_parser
        timestamp:
          parse_from: attributes.time
          layout_type: gotime
          layout: 2006-01-02T15:04:05.999999999Z07:00
        severity:
          parse_from: attributes.level
      - type: move
        from: attributes.msg
        to: body
      - type: trace_parser
        if: attributes.trace_id != nil
        trace_id:
          parse_from: attributes.trace_id
exporters:
  debug:
service:
  pipelines:
    logs:
      receivers: [file_log]
      exporters: [debug]
```

Replace `debug` with the exporter of your backend.

See the [log field reference](https://github.com/byjg/warmphp#logs) for units and request correlation.
