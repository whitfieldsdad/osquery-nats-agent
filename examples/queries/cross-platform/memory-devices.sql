SELECT
    manufacturer,
    serial_number,
    part_number,
    form_factor,
    memory_type,
    ("size" / 1024) AS "size_gb"
FROM memory_devices;
