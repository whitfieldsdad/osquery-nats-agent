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