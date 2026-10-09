# Changelog

## 0.3.1

- **Recall buttons actually appear under the controller device.** 0.3.0 changed the buttons' device but kept their identity, and Home Assistant never moves an existing entity to another device on a discovery update. They are now `button.bose_controller_recall_<id>`; the old `button.bose_<id>_recall` entities are removed automatically.
- **Icons by device type.** ESP blocks are sources (microphone switch, fader level); PowerMatch outputs are speakers (speaker switch, volume level).

## 0.3.0

Entity model reworked for dashboards. Entity IDs change; there is no migration.

- **`enabled` switch replaces `mute`.** On means audio passes. Named after the block alone (`Wireless 1`), icon by kind. Topics `bose/block/<id>/enabled/{state,set}`.
- **Phantom power is a switch** (configuration category) instead of a read-only binary sensor.
- **Preamp gain sensor removed.**
- **Recall buttons moved to the controller device.** Recall now goes to every unit the set writes to (from the `.csp` assignments) instead of only the main, so a set still lands when the main unit's building is powered off. Buttons are unavailable only when none of their units is reachable; partial recalls are logged (`parameter_set_partial`).
- *Last recalled parameter set* is now one diagnostic sensor per unit a set writes to, on that unit, instead of one on the main.

## 0.2.2

- Accept the `;` that ESP and PowerMatch firmware append to `GA` responses (`GA"Wireless 1">3=0.0;`). Previously every level/mute/phantom read was logged as `parameter_unparseable` and never published.

## 0.2.1

- Removed `capture` mode and the `mode`/`capture_dir` options; the add-on no longer maps `/share`. `log_level: debug` logs every device command and response instead.

## 0.2.0

- The ControlSpace Designer `.csp` is now uploaded on the add-on's web UI (ingress) instead of being copied to `/share`. The `design_path` option is gone; the file is validated before anything changes, stored in `/data`, and the bridge restarts with it live.
- The page shows the running design: file, Designer version, every device with address and firmware, block and parameter-set counts.
- Starting without a design no longer exits; the add-on idles with the page available.

## 0.1.0

- Initial release: parses a ControlSpace Designer `.csp`, exposes Gain blocks, ESP inputs, PowerMatch amp outputs and populated parameter sets over MQTT Discovery.
- Polls device state over the ControlSpace Serial Control Protocol (TCP 10055); writes are confirmed by read-back.
- `capture` mode records raw protocol transcripts for every device in the design.
