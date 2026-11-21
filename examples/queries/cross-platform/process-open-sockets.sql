SELECT
    p.pid,
    p.parent AS ppid,
    p.name AS process_name,
    s.fd,
    s.socket,
    s.path AS unix_socket_path,
    CASE WHEN s.family = 2 THEN 'AF_INET'
         WHEN s.family = 10 THEN 'AF_INET6'
         ELSE 'UNKNOWN'
    END AS family_name,
    s.family AS family_number,
    CASE WHEN s.protocol = 6 THEN 'TCP'
         WHEN s.protocol = 17 THEN 'UDP'
         ELSE 'UNKNOWN'
    END AS protocol_name,
    s.protocol AS protocol_number,
    s.local_address,
    s.local_port,
    s.remote_address,
    s.remote_port,
    s.state
FROM process_open_sockets s
JOIN processes p ON s.pid = p.pid
LIMIT 10;