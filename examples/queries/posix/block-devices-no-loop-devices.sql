SELECT
    "uuid",
    "type",
    "label",
    "name",
    "parent",
    "vendor",   -- PCI vendor ID (e.g. 0x15b7 is SanDisk)
    "model",
    "serial",
    "size",
    "block_size"
FROM block_devices
WHERE "name" NOT LIKE '%loop%'
ORDER BY "name";