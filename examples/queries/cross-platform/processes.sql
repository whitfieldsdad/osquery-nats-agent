SELECT
    pid,
    parent AS ppid,
    --upid,
    --uppid,
    "name",
    "path",
    CASE 
        WHEN on_disk = 1 THEN 'yes'
        WHEN on_disk = 0 THEN 'no'
        ELSE 'unknown'
    END AS on_disk,
    virtual_process,
    "cmdline" AS command_line,
    "state",
    "cwd",
    "start_time",
    "elapsed_time",
    "uid",
    gid,
    euid,
    egid,
    suid,
    sgid
FROM processes;