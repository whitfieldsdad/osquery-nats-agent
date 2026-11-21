SELECT 
    "address" AS ip_address, 
    mac AS mac_address, 
    interface AS interface_name,
    IIF(permanent != 0, 1, 0) AS is_static_entry 
FROM arp_cache;
