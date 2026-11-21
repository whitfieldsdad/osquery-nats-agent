# Development guide

## Getting started

### Building 

Building the agent:

```sh
go build -o bin/osquery-nats-agent ./cmd/agent
```

Running the agent, with a configuration file of your own (see [examples/config/](examples/config/)):

```sh
./bin/osquery-nats-agent -config config.json   # add -debug for verbose logging
```

### Running a NATS server

To start a local NATS server in the background:

```sh
docker run -d --rm --name nats -p 4222:4222 nats:latest
```

To specify a 1-hour data retention policy:

```sh
docker run -d --rm --name nats -p 4222:4222 nats:latest -js -store file -dir /data -max_age 1h
```

To specify a 7-day data retention policy:

```sh
docker run -d --rm --name nats -p 4222:4222 nats:latest -js -store file -dir /data -max_age 168h
```

### Directly reading from and writing to NATS

To read from a NATS subject:

```sh
docker exec -it nats nats-sub -s nats://localhost:4222 osquery-queries
```

To write to a NATS subject:

```sh
docker exec -it nats nats-pub -s nats://localhost:4222 osquery-results '<message>'
```

### Querying osquery from the command line

`osqueryi` can be used to execute queries from the command line:

```sh
osqueryi --json "SELECT hostname, cpu_brand, physical_memory FROM system_info;"
```

```json
[
  {"cpu_brand":"AMD Ryzen 9 7900 12-Core Processor             ","hostname":"lab-linux-1","physical_memory":"65504202752"}
]
```

To execute a command across all Linux hosts:

```sh
ansible linux -b -m ansible.builtin.command -a 'osqueryi --json "SELECT name, version FROM os_version;"'
```

```
...
lab-linux-1 | CHANGED | rc=0 >>
[
  {"name":"Ubuntu","version":"26.04.1 LTS (Resolute Raccoon)"}
]
```

To execute a command across all Windows hosts:

```sh
ansible windows -m ansible.windows.win_command -a '"C:\Program Files\osquery\osqueryi.exe" --json "SELECT name, version FROM os_version;"'
```

```
windows-desktop | CHANGED | rc=0 >>
[
  {"name":"Microsoft Windows 11 Pro","version":"10.0.26200"}
]
```