SELECT
    "name",
    "size",                         -- Size of the module in bytes
    "used_by",                      -- Comma delimited list of modules using this module
    LOWER("status") AS "status",    -- Status of the module (e.g., "Live")
    "address"
FROM kernel_modules
ORDER BY "name";
