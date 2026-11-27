SELECT
    "name",
    "version",
    "source"
FROM (
    SELECT
        "name",
        "version",
        'deb' AS "source"
    FROM deb_packages
    UNION ALL
    SELECT
        "name",
        "version",
        'snap' AS "source"
    FROM snap_packages
    UNION ALL
    SELECT
        "name",
        "version",
        'flatpak' AS "source"
    FROM flatpak_packages
    UNION ALL
    SELECT
        "name",
        "version",
        'pacman' AS "source"
    FROM pacman_packages
    UNION ALL
    SELECT
        "name",
        "version",
        'portage' AS "source"
    FROM portage_packages
) ORDER BY "name";