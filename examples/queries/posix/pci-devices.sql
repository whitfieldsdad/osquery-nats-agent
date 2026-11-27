SELECT
    pci_slot,
    pci_class,
    driver,
    vendor_id,
    vendor,
    model_id,
    model,
    pci_class_id,
    pci_subclass_id,
    pci_subclass,
    subsystem_vendor_id,
    subsystem_vendor,
    subsystem_model_id,
    subsystem_model
FROM pci_devices
ORDER BY "model";