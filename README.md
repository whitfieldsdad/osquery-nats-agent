# osquery-nats-sidecar

## Features

- Subscribes to an input NATS subject for SQL queries to execute via `osqueryi` and writes the results to an output NATS subject
- Writes query results to a local file in GZIP compressed JSONL format 

## Usage

```sh
go build -o osquery-nats-sidecar ./cmd/agent
./osquery-nats-sidecar -config config.json   # add -debug for verbose logging
```

If the config file (default: `config.json`) does not exist, the agent writes one with the default settings below and starts with it. An existing config file is never modified.

### Configuration

Example configuration file:

```json
{
    "nats_url": "nats://127.0.0.1:4222",
    "input_subjects": ["osquery-queries"],
    "output_subjects": ["osquery-results"],
    "output_file": "osquery-results.jsonl.gz",
    "query_timeout": "60s"
}
```

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `input_subjects` | yes | `["osquery-queries"]` in a generated config | Subjects to read queries from (NATS wildcards allowed) |
| `output_subjects` | yes | `["osquery-results"]` in a generated config | Subjects to publish every result to; must not overlap `input_subjects` |
| `nats_url` | no | `nats://127.0.0.1:4222` | NATS server URL(s), comma-separated |
| `nats_credentials_path` | no | | Path to a NATS `.creds` file |
| `output_file` | no | `osquery-results.jsonl.gz` | Local file that every result is appended to |
| `osqueryi_path` | no | `osqueryi` (Windows: `C:\Program Files\osquery\osqueryi.exe`) | Path to `osqueryi` |
| `query_timeout` | no | `60s` | Maximum run time of a single query |

The agent continuously reads queries from the `input_subjects` and writes each result to every one of the `output_subjects` and to `output_file`. Every agent subscribed to an input subject runs every query that matches its platform. Unknown configuration fields are rejected.

`output_file` holds one JSON result per line. Each line is written as a separate gzip member, so the file is always readable (`zcat osquery-results.jsonl.gz | jq .`), even if the agent is killed. Results are appended across restarts.

Messages on the input subjects have the following schema:

```json
{
    "query": "SELECT name, version FROM os_version;",
    "platforms": ["darwin", "linux", "windows"]
}
```

Messages on the output subjects have the following schema:

```json
{
    "host": {
        "id": "dbdac958-5491-4b13-8c3d-784428fad940",
        "hostname": "lab-linux-1",
        "network_interfaces": [
            {
                "name": "eth0",
                "ip_addresses": ["127.0.0.1"],
                "mac_address": "00:00:00:00:00:00"
            }
        ],
        "platform": "linux"
    },
    "query": "SELECT name, version FROM os_version;",
    "error": null,
    "results": [
        {
            "name": "Ubuntu",
            "version": "26.04.1 LTS (Resolute Raccoon)"
        }
    ]
}
```

If the agent does not have a matching platform it will silently ignore the message. If `platforms` is omitted or empty, the query runs on every platform.

If the agent encounters an error while executing the query, it will populate the `error` field with the error message and set `results` to `null`.

If a result is larger than the NATS server's max payload (1 MB by default), the agent publishes it with `results` set to `null` and an `error` explaining that the full result is in the local `output_file`.

osquery returns every column value as a string (e.g. `"pid": "1"`).
