SELECT
    u.uuid,
    u.uid,
    u.username,
    u.description AS "user_description",
    u.directory AS "user_directory",
    u.shell AS "user_shell",
    g.gid,
    g.groupname AS "group_name",
FROM user_groups u2g
JOIN users u ON u2g.uid = u.uid
JOIN groups g ON u2g.gid = g.gid
