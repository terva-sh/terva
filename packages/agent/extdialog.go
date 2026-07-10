package agent

// Backs the interactive /extensions dialog (modes.extensionsDialog): a
// dir scan for the full installed list, the two persistence surfaces it
// toggles (manifest `enabled` and project config disable_extensions),
// and a live reload. Plumbed into modes as InteractiveConfig callbacks
// so that package never imports this one.
