WITH RECURSIVE split("name", "size", "status", "address", "required_by", "rest") AS (
    SELECT
        "name",
        "size",
        LOWER("status"),
        "address",
        NULL,
        CASE
            WHEN "used_by" IS NULL OR "used_by" IN ('', '-') THEN ''
            ELSE RTRIM("used_by", ',') || ','
        END
    FROM kernel_modules
    UNION ALL
    SELECT
        "name",
        "size",
        "status",
        "address",
        TRIM(SUBSTR("rest", 1, INSTR("rest", ',') - 1)),
        SUBSTR("rest", INSTR("rest", ',') + 1)
    FROM split
    WHERE "rest" <> ''
)
SELECT
    "name",
    "size",
    NULLIF("required_by", '') AS "required_by",
    "status",
    "address"
FROM split
WHERE "required_by" IS NOT NULL      -- every split item, including empty ones (shown as NULL)
   OR "rest" = ''                    -- modules with no dependents (required_by = NULL)
ORDER BY "name";