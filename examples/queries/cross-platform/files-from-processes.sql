SELECT
    p.pid,
    p.parent AS ppid,
    p."name",
    p."path",
    p."cmdline" AS command_line,
    datetime(f.mtime, 'unixepoch') AS modify_time,
    --datetime(f.atime, 'unixepoch') AS access_time,
    datetime(f.ctime, 'unixepoch') AS change_time,
    --datetime(f.btime, 'unixepoch') AS birth_time,
    h.md5,
    h.sha1,
    h.sha256
FROM processes p
JOIN "file" f ON p.path = f.path
JOIN "hash" h ON f.path = h.path;
