SELECT
    device_id,
    model,
    manufacturer,
    processor_type,
    cpu_status,
    number_of_cores,
    logical_processors,
    address_width,
    current_clock_speed,
    max_clock_speed,
    socket_designation,
    "availability",
    load_percentage,
    number_of_efficiency_cores,
    number_of_performance_cores
FROM cpu_info;