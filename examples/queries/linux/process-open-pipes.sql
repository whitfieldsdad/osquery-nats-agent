SELECT
    pp.type AS pipe_type,
    pp.fd AS pipe_fd,
    pp.mode AS pipe_mode,
    pp.partner_fd AS pipe_partner_fd,
    pp.partner_mode AS pipe_partner_mode,
    pp.inode AS pipe_inode,
    process.pid AS process_pid,
    process.parent AS process_ppid,
    process.name AS process_name,
    process.cmdline AS process_cmdline,
    partner_process.pid AS partner_process_pid,
    partner_process.parent AS partner_process_ppid,
    partner_process.name AS partner_process_name,
    partner_process.cmdline AS partner_process_cmdline
FROM process_open_pipes pp
JOIN processes process ON pp.pid = process.pid
JOIN processes partner_process ON pp.partner_pid = partner_process.pid;