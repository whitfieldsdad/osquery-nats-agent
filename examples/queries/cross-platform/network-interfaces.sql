SELECT
    a.interface AS "name",
    a."type",
    a."address" AS "ip_address",
    a.mask,
    a.broadcast,
    a.point_to_point
FROM interface_addresses a;