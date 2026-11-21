SELECT
    device_id,
    gpu_utilization_pct,
    power_draw_watts,
    allocated_vram,
    in_use_vram,
    temperature_gpu_celsius,
    power_limit_watts,
    fan_speed_pct
FROM gpu_metrics;