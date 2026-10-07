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
mode: bridge
capture_dir: /share/bose/capture
log_level: info
```

| Option | Default | Meaning |
| --- | --- | --- |
| `poll_interval_ms` | `2000` | How often every exposed parameter is re-read from the devices. 500–600000. |
| `write_debounce_ms` | `300` | A level change from Home Assistant is sent once no newer value for that entity has arrived for this long. `0` disables. Mutes and buttons are never delayed. |
| `mode` | `bridge` | `bridge` runs normally. `capture` records raw protocol transcripts for every device and then idles (see below). |
| `capture_dir` | `/share/bose/capture` | Where capture transcripts are written. |
| `log_level` | `info` | `debug` / `info` / `warn` / `error` |

## Devices and entities

Each physical unit is its own Home Assistant device, identified by its Designer node ID.

| Block | Entities |
| --- | --- |
| Gain (ESP) | **level** (number, dB, −60.5…+12) · **mute** (switch) |
| Input (ESP) | **level** · **mute** · *preamp gain*, *phantom power* (diagnostic, read-only) |
| Amp Output (PowerMatch) | **level** (dB, −60.5…0) · **mute** |
| Parameter set | **Recall …** button on the main ESP · *Last recalled parameter set* (diagnostic) |

The **Bose ControlSpace Controller** device carries a *Loaded design* sensor (Designer version, with the file name, counts and load time as attributes) and one *… connection* sensor per unit (`connected` / `disconnected`, with IP, port and last error as attributes).

Entity names are the block labels from Designer, verbatim. Rename them in Home Assistant if you prefer; that is independent of the design file.

## How writes work

Home Assistant is never the source of truth. The wall panels and the ControlSpace Remote app keep working; the add-on mirrors whatever the devices report.

- A **level** change is debounced, sent as a module command, acknowledged by the device, **read back**, and only then published. Dragging a slider results in one write.
- A **mute** change is sent immediately, acknowledged, read back, published.
- A **Recall** button sends the system command to the main ESP and then asks it which set it last recalled. Parameter sets have no acknowledgement in the protocol, so this read-back is the only confirmation.

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
| `bose/device/<id>/parameter_set/{state,attributes}` | last recalled set (main ESP) |
| `bose/block/<id>/level/{state,set}` | level in dB |
| `bose/block/<id>/mute/{state,set}` | ON/OFF |
| `bose/block/<id>/{gain,phantom}/state` | input diagnostics |
| `bose/parameter_set/<n>/press` | recall button |

Discovery configs under `homeassistant/<component>/bose_<id>/<object>/config`; device identifier `bose:<id>`; unique IDs `bose_<id>_<object>`. State is retained, commands are not, and retained messages are never acted on.

## Capture mode

Set `mode: capture`, restart, and the add-on connects to each unit in turn, probes subscription support (`SUB`), reads every exposed parameter, re-sends the first block's current level and mute unchanged (to record how the device acknowledges), and writes:

```text
/share/bose/capture/<timestamp>/
  summary.json
  ESP_Main-100001.log
  …
```

Each `.log` line is `<time> > <command sent>` or `<time> < <hex bytes>  |<printable>|`. The add-on then idles; set `mode: bridge` and restart. Capture is read-only apart from the unchanged write-back. Capture uses the design uploaded in bridge mode; upload one first.

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| `design_unavailable` | No design has been uploaded yet, or the stored one no longer parses. Open the add-on page and upload the `.csp`. |
| Upload rejected | The page shows the parser's reason. The file must be a ControlSpace Designer project containing at least one ESP or PowerMatch. |
| Unit `disconnected`, `connection refused` / timeout | The HA host can't reach `<ip>:10055`. Static IPs are read from the design; confirm they match reality and that routing/firewall allow TCP 10055. |
| `parameter_refused … NAK 01` | The device doesn't know that module label — the design on the device differs from the file you uploaded. Push the design from Designer or upload the matching `.csp`. |
| `parameter_set_not_recalled` | The main ESP reported a different set than requested. Check the set is populated on the device. |
| Entities missing after a design change | Expected if the block was deleted and recreated (new node ID): the old entity is removed and a new one appears. |
| `mqtt_publish_failed` / no entities | Mosquitto not running or the MQTT integration not configured. |

## Limitations

- Polling only; subscriptions (`SUB`) are probed in capture mode but not used yet
- Exposes Gain, ESP Input and Amp Output blocks and populated parameter sets; no ESP outputs, mixers, EQ, ControlSpace Groups, amp standby, metering or source selection
- One design at a time
- The device's stored design is not retrieved automatically (Designer protocol on port 10001 is undocumented)
