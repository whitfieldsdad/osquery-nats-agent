SELECT
    pid,
    port,
    protocol,
    family,
    "address",
    fd,
    socket,
    "path",
    net_namespace
FROM listening_ports;
