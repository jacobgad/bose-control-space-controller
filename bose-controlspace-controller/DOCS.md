# Bose ControlSpace Controller

Makes a Bose ControlSpace system — ESP processors and PowerMatch amplifiers — appear as MQTT devices in Home Assistant. Zone gains, individual inputs, amplifier outputs and parameter-set presets become entities; Home Assistant never has to know the serial protocol.

## Installation

1. **Settings → Add-ons → Add-on Store → ⋮ → Repositories**, add this repository's URL.
2. Install **Bose ControlSpace Controller** and **Start** it.
3. Click **Open Web UI** (or the sidebar entry) and upload your ControlSpace Designer project file (`.csp`).
4. Watch the **Log** for `design_loaded` and `device_connected`. Devices appear under **Settings → Devices & services → MQTT**.

Requires the **Mosquitto broker** add-on and the **MQTT integration**. Broker credentials are read from the Supervisor; there is nothing to enter.

## The design file is the configuration

Everything the add-on exposes is derived from the uploaded `.csp`:

- **Devices** — every ESP and PowerMatch node, with its IP address and serial-over-Ethernet port (normally 10055).
- **Blocks** — every Gain block and Input on an ESP, and every Amp Output on a PowerMatch, with the level range Designer declares.
- **Parameter sets** — those that actually contain assignments.

There are no allow-lists or renames in the add-on. To hide an entity, disable it in Home Assistant.

### Uploading and updating

The add-on's page shows the running design — file name, Designer version, every device with its address and firmware, block and parameter-set counts — and an upload form.

Uploading a file checks it first; a file that is not a Designer project, or has no ESP/PowerMatch devices, is rejected and the running design is untouched. A valid file is stored in the add-on's private data (so it survives restarts and is included in backups), the bridge restarts with it, and the page confirms. No add-on restart is needed. Only one design is kept: a new upload replaces the previous one.

When the Bose design changes, export from Designer and upload: entities whose block still exists (same Designer node ID) keep their identity even if renamed; entities for blocks that were removed are deleted from Home Assistant and listed in the log as `discovery_removed`.

Blocks the protocol cannot address (two modules on one device with the same label) are skipped with a `block_skipped` warning.

Until a design has been uploaded the add-on runs idle with the page available and logs `design_unavailable`.

## Configuration

```yaml
poll_interval_ms: 2000
write_debounce_ms: 300
log_level: info
```

| Option | Default | Meaning |
| --- | --- | --- |
| `poll_interval_ms` | `2000` | How often every exposed parameter is re-read from the devices. 500–600000. |
| `write_debounce_ms` | `300` | A level change from Home Assistant is sent once no newer value for that entity has arrived for this long. `0` disables. Switches and buttons are never delayed. |
| `log_level` | `info` | `debug` / `info` / `warn` / `error` |

## Devices and entities

Each physical unit is its own Home Assistant device, identified by its Designer node ID.

| Block | Entities |
| --- | --- |
| Gain (ESP) | **level** (number, dB, −60.5…+12) · **enabled** (switch) |
| Input (ESP) | **level** · **enabled** · *phantom power* (switch, configuration) |
| Amp Output (PowerMatch) | **level** (dB, −60.5…0) · **enabled** |
| Parameter set | **Recall …** button on the controller device · *Last recalled parameter set* (diagnostic) on each unit the set writes to |

The **enabled** switch is the block's mute, inverted: on means audio passes. A dashboard of enabled switches therefore shows what is live. It is named after the block alone (`Wireless 1`); the level is `Wireless 1 level`.

**Phantom power** is a real switch because the protocol allows it, but it sits in the device's *Configuration* section and is left off auto-generated dashboards: switching it on a line-level or wireless source can damage equipment.

Recall buttons belong to the **Bose ControlSpace Controller** device rather than to any unit, because a set is one intent applied to several units; what each unit last recalled is a fact about that unit and lives on it. The controller device also carries a *Loaded design* sensor (Designer version, with the file name, counts and load time as attributes) and one *… connection* sensor per unit (`connected` / `disconnected`, with IP, port and last error as attributes).

Entity names are the block labels from Designer, verbatim. Rename them in Home Assistant if you prefer; that is independent of the design file.

## How writes work

Home Assistant is never the source of truth. The wall panels and the ControlSpace Remote app keep working; the add-on mirrors whatever the devices report.

- A **level** change is debounced, sent as a module command, acknowledged by the device, **read back**, and only then published. Dragging a slider results in one write.
- An **enabled** or **phantom power** change is sent immediately, acknowledged, read back, published.
- A **Recall** button sends the system command to every unit that set writes to (worked out from the assignments in the `.csp`), then asks each which set it last recalled. Parameter sets have no acknowledgement in the protocol, so this read-back is the only confirmation. Units that are powered off are skipped and logged (`parameter_set_partial`); a button is *unavailable* only when none of its units is reachable.

Each unit's *Last recalled parameter set* sensor shows what that unit last heard (`none` since power-up). After one building has been powered down and up, its units can lag the others until the next recall; the sensors show exactly which.

Nothing is published optimistically. A value out of the block's range is rejected and logged (`level_out_of_range`) without contacting the device.

## Polling

Every parameter is re-read on `poll_interval_ms`, one persistent TCP connection per unit. Only changed values are published, so Home Assistant sees no churn. Changes made from a CC-64, a phone or Designer appear within one interval. Because writes are confirmed by read-back, the interval can be long (10 s or more) without affecting how responsive Home Assistant feels.

The connection is dropped whenever ControlSpace Designer goes online with a device; the add-on reconnects on the next poll. While a unit is unreachable its entities are unavailable and its connection sensor shows the error.

## MQTT contract

Prefix `bose/`. `<id>` is the six-digit Designer node ID.

| Topic | Purpose |
| --- | --- |
| `bose/controller/availability` | bridge online/offline; also the Last Will |
| `bose/controller/design/{state,attributes}` | loaded design version and metadata |
| `bose/device/<id>/availability` | unit reachable |
| `bose/device/<id>/connection/{state,attributes}` | connected/disconnected, ip, port, last_error |
| `bose/device/<id>/parameter_set/{state,attributes}` | last recalled set on that unit |
| `bose/block/<id>/level/{state,set}` | level in dB |
| `bose/block/<id>/enabled/{state,set}` | ON = unmuted |
| `bose/block/<id>/phantom/{state,set}` | ON/OFF (inputs) |
| `bose/parameter_set/<n>/press` | recall button |
| `bose/parameter_set/<n>/availability` | any unit the set writes to is reachable |

Discovery configs under `homeassistant/<component>/bose_<id>/<object>/config`; device identifier `bose:<id>`; unique IDs `bose_<id>_<object>`. State is retained, commands are not, and retained messages are never acted on.

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| `design_unavailable` | No design has been uploaded yet, or the stored one no longer parses. Open the add-on page and upload the `.csp`. |
| Upload rejected | The page shows the parser's reason. The file must be a ControlSpace Designer project containing at least one ESP or PowerMatch. |
| Unit `disconnected`, `connection refused` / timeout | The HA host can't reach `<ip>:10055`. Static IPs are read from the design; confirm they match reality and that routing/firewall allow TCP 10055. |
| `parameter_refused … NAK 01` | The device doesn't know that module label — the design on the device differs from the file you uploaded. Push the design from Designer or upload the matching `.csp`. |
| `parameter_set_recall_failed … device reports set n` | That unit reported a different set than requested. Check the set is populated on the device. |
| `parameter_set_partial` / `parameter_set_unreachable` | Some or all of the units the set writes to were off. Units that were reached have applied it. |
| `parameter_set_no_target_device` | The set's assignments don't touch any ESP or PowerMatch in the design (e.g. only a wall panel). |
| Entities missing after a design change | Expected if the block was deleted and recreated (new node ID): the old entity is removed and a new one appears. |
| `mqtt_publish_failed` / no entities | Mosquitto not running or the MQTT integration not configured. |

## Limitations

- Polling only; the protocol's subscription feature is not used
- Exposes Gain, ESP Input and Amp Output blocks and populated parameter sets; no ESP outputs, mixers, EQ, ControlSpace Groups, Parameter Set Lists, amp standby, metering or source selection
- Sending `SS`/`GS` to the units a set writes to rather than only the main relies on the protocol's statement that system commands may be sent to any unit involved in the construct; not yet validated on hardware
- One design at a time
- The device's stored design is not retrieved automatically (Designer protocol on port 10001 is undocumented)
