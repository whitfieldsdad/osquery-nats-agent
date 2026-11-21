# Development guide

## Getting started

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