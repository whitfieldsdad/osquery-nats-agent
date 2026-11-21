# osquery-nats-agent

An agent that reads SQL queries from input files and/or NATS subjects, executes them via `osqueryi`, and writes the results to output files and/or NATS subjects.

![Architecture](docs/simple-deployment.png)

## Features

- Configure the agent with a JSON or YAML file
- Read SQL queries from input files or NATS subjects
- Write query results to output files or NATS subjects
- Dynamically partition<sub>1</sub> NATS subject names and input/output file paths using Jinja2 template strings (e.g. to create one NATS subject per OS type, or one output file per date and IP address)
- Execute queries on an interval (e.g. to check for new processes every second)
- Server to agent messages are JSON objects containing the query and optional platform restrictions (e.g. `{"query": "SELECT name, version FROM os_version;"}`).
- Agent to server messages are always in JSON format and include the host metadata, the executed query, any error encountered, and the query results.
- Results are differential: each run of a query reports only the rows added and removed since its previous run, and a run in which nothing changed reports nothing.

<sub>1. Subjects are created by NATS as soon as they're written to.</sub>

## Example use cases

| Name | Description | Notes |
|------|-------------|------|
| Process monitoring | Track metadata of running processes, including process image metadata (e.g. the MD5, SHA-1, and SHA-256 hash of the file). | |
| File integrity monitoring | Watch file and/or directories for changes. Level of granularity is set by the user defined queries (i.e. you could include file checksums, timestamps, permissions, etc.). | |

## Usage

### Building the agent

To build the agent:

```sh
make build
```

Or, equivalently:

```sh
mkdir -p bin/
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o bin/osquery-nats-agent-linux-amd64 ./cmd/agent
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build -ldflags="-s -w" -o bin/osquery-nats-agent-darwin-amd64 ./cmd/agent
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags="-s -w" -o bin/osquery-nats-agent-windows-amd64.exe ./cmd/agent
```

### Running the agent

The agent needs a configuration file, passed with `-config`; `config.json` below stands for yours. See [agent configuration](docs/agent-configuration.md) for the options, and [`examples/config/`](examples/config/) for configurations to start from.

If running from source:

```sh
go run ./cmd/agent -config config.json
```

If using a compiled binary:

```sh
./bin/osquery-nats-agent-linux-amd64 -config config.json
```

To enable debug logging, add the `-debug` flag when running the agent:

```sh
./bin/osquery-nats-agent-linux-amd64 -config config.json -debug
```
