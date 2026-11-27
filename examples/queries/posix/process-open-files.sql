SELECT
    pf.pid,
    pf.fd,
    pf.path
FROM process_open_files pf
JOIN processes p ON pf.pid = p.pid
JOIN "file" f ON pf.path = f.path;