# Agent configuration

## Configuration options

The agent supports a wide range of configuration options.

The configuration file can be JSON or YAML: a file named `*.yaml` or `*.yml` is read as YAML, and any other file as JSON. Both formats use the same keys, so these are equivalent:

```json
{"input_subjects": ["osquery.queries"], "output_subjects": ["osquery.results"], "query_timeout": "30s"}
```

```yaml
input_subjects: [osquery.queries]
output_subjects: [osquery.results]
query_timeout: 30s
```

Queries can come from NATS subjects (`input_subjects`), input files (`input_files`), scheduled queries (`scheduled_queries`) or any mix of them, and results can go to NATS subjects (`output_subjects`), output files (`output_files`) or both. A configuration must have at least one source of queries and at least one destination for results. The agent only connects to NATS if `input_subjects` or `output_subjects` is set.

Results are written to `data/{{ date }}/osquery-results.jsonl.gz` unless `output_files` is set, so to send results only to NATS, set `"output_files": []`.

The agent never reads its own output files as input files: it will not start if an `input_files` pattern matches one.

Using NATS for input and/or output:

| Property | Description | Default |
|---------|-------------|---------|
| `nats_url` | NATS server URL | `nats://127.0.0.1:4222` |
| `nats_credentials_path` | Optional path to a NATS .creds file | |
| `nats_tls_cert_path` | Optional PEM client certificate for mutual TLS | |
| `nats_tls_key_path` | Optional PEM private key for mutual TLS | |
| `nats_tls_ca_path` | Optional PEM file of CA certificates to verify the NATS server's certificate | |
| `input_subjects` | List of NATS subjects to subscribe to for incoming queries | `[]` |
| `output_subjects`| List of NATS subjects to publish query results to | `[]` |

Using files for input and/or output:

| Property | Description | Default |
|---------|-------------|---------|
| `input_files` | List of input files containing queries (see [input file formats](#input-file-formats)) | `[]` |
| `output_files` | List of gzip-compressed JSONL files every query result is appended to (`[]` for none) | `["data/{{ date }}/osquery-results.jsonl.gz"]` |
| `input_file_poll_interval` | Interval at which to poll input files for new queries | `15s` |

Agent behaviour:

| Property | Description | Default |
|---------|-------------|---------|
| `query_timeout` | Timeout for query execution | `60s` |
| `snapshot` | If `true`, every run of every query reports all of the rows it returned (see [snapshot results](data-format.md#snapshot-results)). If `false`, each run reports only the rows added and removed since the query's previous run (see [differential results](data-format.md#differential-results)). | `false` |
| `max_cached_queries` | How many queries received over NATS or read from input files keep their previous results, to report only what changed on their next run (see [differential results](data-format.md#differential-results)). When there are more, the least recently run query's results are dropped, and its next run reports every row. Scheduled queries always keep theirs. Has no effect when `snapshot` is `true`. | `10000` |

Automation:

| Property | Description | Default |
|---------|-------------|---------|
| `scheduled_queries` | List of queries to run on a schedule | `[]` |

### Input file formats

Each query request in an input file is a JSON object in the [same format as a message on an input subject](data-format.md), e.g. `{"query": "SELECT name, version FROM os_version;", "platforms": ["linux"]}`. Plain SQL is not accepted. Any file can be gzip-compressed; this is detected from its contents.

| Format | File names | Contents | New queries |
|--------|------------|----------|-------------|
| JSON | `*.json`, `*.json.gz` | An array of query requests, a single request, or a sequence of either. May be pretty-printed. | The file is read whole whenever it changes. Requests added after the ones already read are run; if any already-read request changes, every request in the file is run again. Removing requests runs nothing. A file that is not valid JSON (e.g. one still being written) is skipped until it changes. |
| JSONL | `*.jsonl`, `*.jsonl.gz`, and any other name | One query request per line. Blank lines are skipped. | Lines appended to the file are run. If the lines already read change, or the file is replaced or truncated, every line is run again. |

### Configuration template properties

Configuration template properties are used to dynamically inject host metadata and time-based values into NATS subject names and output file paths.

This allows you to dynamically create NATS subject names and output file paths based on host metadata and the current time (e.g. to create one NATS subject per OS type, or one output file per IP address per date).

#### Host metadata template properties

| Name | Description | Examples |
|------|-------------|----------|
| `platform` | OS type | `darwin`, `linux`, `windows` |
| `hostname` | Hostname | `my-host` |
| `host_id` | Host ID (osquery's `system_info.uuid`) | `550e8400-e29b-41d4-a716-446655440000` |
| `ip_address` | IP address of the network interface used for the default route | `192.168.1.10` |
| `mac_address` | MAC address of the network interface used for the default route | `00:1A:2B:3C:4D:5E` |

Examples:

```
osquery-results.{{ platform }}
osquery-results.{{ hostname }}
osquery-results.{{ ip_address | replace('.', '-') }}
osquery-results.{{ mac_address | replace(':', '') }}
```

#### Time-based template properties

| Name | Description | Examples |
|------|-------------|----------|
| `date` | Current UTC date | `2026-11-20` |
| `time` | Current UTC timestamp | `2026-11-20T12:34:56` |
| `clock_time` | Current UTC time | `12:34:56` |

## Example configurations

### Input and output files

To poll an input file every second:

```json
{
  "input_files": [
    "data/queries.json"
  ],
  "output_files": [
    "data/results.jsonl.gz"
  ],
  "input_file_poll_interval": "1s"
}
```

Example input file:

`data/queries.json`:

```json
[
  {
    "query": "SELECT name, version FROM os_version;",
    "platforms": ["darwin", "linux", "windows"]
  }
]
```

Example output file:

```sh
zcat data/results.jsonl.gz | jq .
```

```json

```

### NATS input/output partitioned by OS type

```json
{
    "nats_url": "nats://127.0.0.1:4222",
    "input_subjects": [
      "osquery.results", 
      "osquery.results.{{ platform }}"
    ],
    "output_subjects": [
      "osquery.results", 
      "osquery.results.{{ platform }}"
    ],
    "query_timeout": "60s"
}
```

### Basic process monitor with NATS output partitioned by OS type

- Lists running processes
- Lists file metadata for running processes

```yaml
nats_url: nats://127.0.0.1:4222
output_subjects:
  - osquery.results
  - osquery.results.{{ platform }}
query_timeout: 60s
scheduled_queries:
  - query: |
      SELECT
          pid,
          parent AS ppid,
          "name",
          "path",
          "cmdline" AS command_line,
          "state",
          "cwd",
          "start_time"
      FROM processes;
    interval: 1
    platforms: [darwin, linux, windows]
  - query: |
      SELECT
          p.pid,
          p.parent AS ppid,
          p."name",
          p."path",
          p."cmdline" AS command_line,
          h.md5,
          h.sha1,
          h.sha256
      FROM processes p
      JOIN "file" f ON p.path = f.path
      JOIN "hash" h ON f.path = h.path;
    interval: 1
    platforms: [darwin, linux, windows]
```

```json
{
  "nats_url": "nats://127.0.0.1:4222",
  "input_subjects": [
    "osquery.queries", 
    "osquery.queries.{{ platform }}"
  ],
  "output_subjects": [
    "osquery.results", 
    "osquery.results.{{ platform }}"
  ],
  "query_timeout": "60s",
  "scheduled_queries": [
    {
      "query": "SELECT pid, parent AS ppid, \"name\", \"path\", \"cmdline\" AS command_line, \"state\", \"cwd\", \"start_time\" FROM processes;",
      "interval": 1,
      "platforms": ["darwin", "linux", "windows"]
    },
    {
      "query": "SELECT p.pid, p.parent AS ppid, p.\"name\", p.\"path\", p.\"cmdline\" AS command_line, h.md5, h.sha1, h.sha256 FROM processes p JOIN \"file\" f ON p.path = f.path JOIN \"hash\" h ON f.path = h.path;",
      "interval": 1,
      "platforms": ["darwin", "linux", "windows"]
    }
  ]
}
```