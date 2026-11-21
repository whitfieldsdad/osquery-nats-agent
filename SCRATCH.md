### Configuration

Example configuration file with an input and output subject:

```json
{
    "nats_url": "nats://127.0.0.1:4222",
    "input_subjects": ["osquery-queries"],
    "output_subjects": ["osquery-results"],
    "query_timeout": "60s"
}
```

Example configuration file with an input and output subject, output file, and scheduled queries:

```json
{
    "nats_url": "nats://127.0.0.1:4222",
    "input_subjects": ["osquery-queries"],
    "output_subjects": ["osquery-results"],
    "output_files": ["data/{{ date }}/osquery-results.jsonl.gz"],
    "query_timeout": "60s",
    "scheduled_queries": [
        {
            "query": "SELECT name, version FROM os_version;",
            "interval": 60,
            "platforms": ["darwin", "linux", "windows"]
        }
    ]
}
```

Every result is appended to each file in `output_files`, a list of gzip-compressed JSONL (`.jsonl.gz`) files. For example, to keep one file per day and a second copy partitioned by host:

```json
{
    "input_subjects": ["osquery-queries"],
    "output_subjects": ["osquery-results"],
    "output_files": [
        "data/{{ date }}/osquery-results.jsonl.gz",
        "data/{{ host_id }}/{{ date }}/osquery-results.jsonl.gz"
    ]
}
```

- If `output_files` is omitted, results are written to `data/{{ date }}/osquery-results.jsonl.gz`. Set it to `[]` to write no output files.
- Each result is compressed once and appended to every file as a complete gzip member, so every file stays readable with `zcat` after every write. Files are opened in append mode, so results are added to existing files when the agent restarts.
- If two entries render to the same path, each result is written to that file once.
- If a result can't be written to one file, it's still written to the others, and the failure is logged.

Example configuration file that also reads queries from input files:

```json
{
    "nats_url": "nats://127.0.0.1:4222",
    "input_subjects": ["osquery-queries"],
    "output_subjects": ["osquery-results"],
    "input_files": [
        "queries/*.jsonl.gz",
        "queries/{{ platform }}/*.jsonl.gz"
    ],
    "input_file_poll_interval": "15s"
}
```

Input files are JSONL files of queries: each line is a query in the same format as an [inbound message](#data-format), so either a JSON object or a single line of plain SQL (a multi-line query must be JSON-encoded). Files can be gzip-compressed (`.jsonl.gz`, including files made of concatenated gzip members such as the agent's own output files) or plain text. Blank lines are skipped, and malformed lines are logged and skipped.

Each `input_files` entry is a glob pattern and can use the [host metadata template properties](#host-metadata-template-properties). The patterns are polled for new queries when the agent starts and then every `input_file_poll_interval`: every query in a newly matched file is run, as is every line appended to a file that has already been read, and the results are delivered like any other query's. Patterns that match no files are logged as a warning.

| Setting | Description | Default |
|---------|-------------|---------|
| `input_file_poll_interval` | How long to wait between polls of the input files, e.g. `"30s"` or `"5m"`. | `"15s"` |

Polls never overlap: the wait starts once the previous poll's queries have finished.

How changes to input files are handled:

- The agent remembers how many lines of each file it has read, and only runs the lines after those. To add queries, append lines (or, for a `.jsonl.gz` file, append gzip members) to a file, or add a new file.
- A file whose size and modification time haven't changed since it was last read is not read again.
- A file that has changed is checked before any of its queries are run: the agent compares the SHA-256 checksum of the part of the file it has already read with the checksum it recorded then. If they match, the file was appended to and only the new lines are run. If they differ, the file was rewritten, and every query in it is run again from the start.
- A file that is replaced by a different file (e.g. written elsewhere and renamed into place), or that gets smaller, is read again from the start.
- A last line without a trailing newline may still be being written, so it is run only once the file has gone a whole poll without changing.
- A file that stops matching its pattern is forgotten, so if it is matched again later it is read from the start.
- What has been read is kept in memory only: every query in the input files is run again each time the agent restarts.

To create an input file:

```sh
mkdir -p queries
printf '%s\n' \
    '{"query": "SELECT name, version FROM os_version;"}' \
    '{"query": "SELECT pid, name, path FROM processes;", "platforms": ["linux", "darwin"]}' \
    | gzip > queries/example.jsonl.gz
```

Example configuration file that connects to NATS over mutually authenticated TLS (mTLS):

```json
{
    "nats_url": "tls://nats.example.com:4222",
    "nats_tls_cert_path": "/etc/osquery-nats-agent/client-cert.pem",
    "nats_tls_key_path": "/etc/osquery-nats-agent/client-key.pem",
    "nats_tls_ca_path": "/etc/osquery-nats-agent/ca.pem",
    "input_subjects": ["osquery-queries"],
    "output_subjects": ["osquery-results"]
}
```

| Setting | Description |
|---------|-------------|
| `nats_tls_cert_path` | PEM client certificate presented to the NATS server. Must be set together with `nats_tls_key_path`. |
| `nats_tls_key_path` | PEM private key for the client certificate. |
| `nats_tls_ca_path` | PEM file of CA certificates used to verify the NATS server's certificate. If omitted, the system's trusted roots are used. |

Setting any of these requires TLS: the agent will not connect to a NATS server that doesn't offer it. They can be combined with `nats_credentials_path`. The NATS server must be configured to require client certificates, e.g.:

```
tls {
  cert_file: "server-cert.pem"
  key_file:  "server-key.pem"
  ca_file:   "ca.pem"  # CA that signed the agents' client certificates
  verify:    true      # or verify_and_map: true to map certificates to NATS users
}
```

Example configuration file with auto-partitioned input and output subjects:

Input subjects:

| Template string | Description | Examples |
| ------ | -------- | -------- |
| `osquery-queries.{{ platform }}` | OS type | `osquery-queries.windows`, `osquery-queries.linux`, `osquery-queries.darwin` |
| `osquery-queries.{{ hostname }}` | Hostname | `osquery-queries.example-host` |
| `osquery-queries.{{ ip_address.replace('.', '-') }}` | IP address of the network interface used for the default route | `osquery-queries.192-168-1-10` |
| `osquery-queries.{{ mac_address }}` | MAC address of the network interface used for the default route | `osquery-queries.00:1A:2B:3C:4D:5E` |
| `osquery-queries.{{ host_id }}` | Host ID (osquery's `system_info.uuid`) | `osquery-queries.550e8400-e29b-41d4-a716-446655440000` |

Output subjects:

| Template string | Description | Examples |
| ------ | -------- | -------- |
| `osquery-results.{{ platform }}` | OS type | `osquery-results.windows`, `osquery-results.linux`, `osquery-results.darwin` |
| `osquery-results.{{ hostname }}` | Hostname | `osquery-results.example-host` |
| `osquery-results.{{ ip_address | replace('.', '-') }}` | IP address of the network interface used for the default route | `osquery-results.192-168-1-10` |
| `osquery-results.{{ mac_address }}` | MAC address of the network interface used for the default route | `osquery-results.00:1A:2B:3C:4D:5E` |
| `osquery-results.{{ host_id }}` | Host ID (osquery's `system_info.uuid`) | `osquery-results.550e8400-e29b-41d4-a716-446655440000` |

```json
{
    "nats_url": "nats://127.0.0.1:4222",
    "input_subjects": [
        "osquery-queries",
        "osquery-queries.{{ platform }}",
        "osquery-queries.{{ hostname }}",
        "osquery-queries.{{ ip_address | replace('.', '-') }}",
        "osquery-queries.{{ mac_address }}",
        "osquery-queries.{{ host_id }}"
    ],
    "output_subjects": [
        "osquery-results.{{ platform }}",
        "osquery-results.{{ hostname }}",
        "osquery-results.{{ ip_address | replace('.', '-') }}",
        "osquery-results.{{ mac_address }}",
        "osquery-results.{{ host_id }}"
    ],
    "scheduled_queries": [
        {
            "name": "example_query",
            "query": "SELECT name, version FROM os_version;",
            "interval": 60,
            "platforms": ["darwin", "linux", "windows"]
        }
    ],
    "output_files": [
        "data/{{ date }}/{{ host_id }}/osquery-results.jsonl.gz",
        "data/{{ date }}/{{ hostname }}/osquery-results.jsonl.gz",
        "data/{{ date }}/{{ ip_address | replace('.', '-') }}/osquery-results.jsonl.gz",
        "data/{{ date }}/{{ mac_address }}/osquery-results.jsonl.gz",
        "data/{{ date }}/{{ host_id }}/osquery-results.jsonl.gz"
    ],
    "query_timeout": "60s"
}
```

Input subjects, output subjects, `input_files` and `output_files` are [Jinja2](https://jinja.palletsprojects.com/en/stable/templates/) templates, so [filters](https://github.com/NikolaLohinski/gonja) such as `replace`, `lower` and `upper` can be used.

 The following variables are available:

- `{{ platform }}`: OS type (`darwin`, `linux`, `windows`)
- `{{ hostname }}`: Hostname
- `{{ host_id }}`: Host ID (osquery's `system_info.uuid`)
- `{{ ip_address }}`: IP address of the network interface used for the default route
- `{{ mac_address }}`: MAC address of the network interface used for the default route
- `{{ date }}`: current UTC date, e.g. `2026-11-20` (`output_files` only)
- `{{ time }}`: current UTC timestamp, e.g. `2026-11-20T12:34:56` (`output_files` only)
- `{{ clock_time }}`: current UTC time, e.g. `12:34:56` (`output_files` only)

Example subject partitioning schemes based on host metadata:

```
osquery-results.{{ platform }}
osquery-results.{{ hostname }}
osquery-results.{{ hostname.lower() }}
osquery-results.{{ ip_address }}
osquery-results.{{ mac_address }}
osquery-results.{{ mac_address | replace(':', '') }}
```

Example subject partitioning schemes based on dates and times:

```
osquery-results.{{ date }}
osquery-results.{{ date }}-{{ time[:2] }}
osquery-results.{{ date }}-{{ time | replace(':', '') }}
```
