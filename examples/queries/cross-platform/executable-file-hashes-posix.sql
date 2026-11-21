SELECT
    "path",
    directory,
    md5,
    sha1,
    sha256
FROM "hash"
WHERE directory IN ('/usr/bin', '/bin', '/sbin', '/usr/sbin', '/usr/local/bin');
