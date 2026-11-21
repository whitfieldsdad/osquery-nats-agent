SELECT
    iso_8601 AS "datetime",
    date("datetime") AS "date",
    "timestamp",
    unix_time,
    local_timezone,
    "year",
    "month",
    "day",
    "hour",
    "minutes" AS "minute",
    "seconds" AS "second"
FROM "time";