# Bose ControlSpace Controller add-on repository

Home Assistant add-on that exposes a **Bose ControlSpace** system — ESP processors and PowerMatch amplifiers — as native MQTT devices. Zone gains, inputs, amplifier outputs and parameter-set presets become Home Assistant entities; scenes, automations and dashboards are Home Assistant's job. The add-on is only the hardware driver.

[![Add repository to my Home Assistant](https://my.home-assistant.io/badges/supervisor_add_addon_repository.svg)](https://my.home-assistant.io/redirect/supervisor_add_addon_repository/?repository_url=https%3A%2F%2Fgithub.com%2Fjacobgad%2Fbose-control-space-controller)

Or manually: **Settings → Add-ons → Add-on Store → ⋮ → Repositories**, add `https://github.com/jacobgad/bose-control-space-controller`.

| Add-on | |
| --- | --- |
| [Bose ControlSpace Controller](bose-controlspace-controller/) | Install, start, open the web UI and upload your ControlSpace Designer `.csp`. User documentation: [DOCS.md](bose-controlspace-controller/DOCS.md). |

## How it works

```text
Home Assistant ──MQTT──▶ Mosquitto ◀──MQTT── ControlSpace Controller ──TCP 10055──▶ ESP / PowerMatch
      │ ingress                                │
      └──────── upload .csp ───────────────────┤
                                               ├─ parses the Designer .csp (devices, blocks, parameter sets)
                                               ├─ publishes MQTT Discovery, keyed on Designer node IDs
                                               ├─ polls every parameter; publishes on change
                                               └─ writes: debounce → SA → ACK → GA read-back → publish
```

- **The `.csp` is the single source of truth.** IPs, labels, ranges and presets come from it; there is no second configuration layer. Curation is Home Assistant's entity enable/disable.
- **Identity is the Designer node ID.** Labels can be renamed between design versions; node IDs survive. Entities keep their history across a rename.
- **Home Assistant is never the source of truth.** Wall panels and the Remote app keep writing; the add-on mirrors the devices and never publishes optimistically.
- **Upload to change.** The ingress page validates a new `.csp`, stores it in `/data`, and swaps the running bridge; stale entities are removed, new ones appear.

Single static Go binary, ~17 MB image. One ingress page (status + upload), no REST API, no custom integration, no database.

## Hardware and protocol

Speaks the documented [ControlSpace Serial Control Protocol v5.13](https://assets.boseprofessional.com/m/4998082f60dfee56/original/ControlSpace-Serial-Protocol-v5-13.pdf) over serial-over-Ethernet. Commands used:

| Command | Purpose |
| --- | --- |
| `GA "Label">n` / `SA "Label">n=v` | read / write a module parameter (Gain 1,2 · Input 2,3,4,5 · Amp Output 1,2) |
| `SS n` / `GS` | recall / query parameter set (main ESP only) |
| `SUB`, `GC`, `GY`, `GF` | capture-mode probes only |

Developed against the protocol document; **not yet validated on hardware**. The first on-site step is `mode: capture`, which records every exchange so the parser's assumptions (ACK framing, response formats, subscription support) can be checked against real devices and turned into fixtures.

## Development

Requires Go ≥ 1.27, [golangci-lint](https://golangci-lint.run) v2, Docker for images.

```bash
cd bose-controlspace-controller
go vet ./... && golangci-lint run ./... && go test -race ./...
CGO_ENABLED=0 go build -o bose-controlspace-controller .
```

Run against any broker (`MQTT_HOST` set → env config; unset → Supervisor services API). The page is on `:8099`; upload a `.csp` there, or pre-place one in the design directory:

```bash
echo '{}' > /tmp/options.json
MQTT_HOST=127.0.0.1 BOSE_OPTIONS_PATH=/tmp/options.json BOSE_MANIFEST_PATH=/tmp/manifest.json BOSE_DESIGN_DIR=/tmp/bose-design ./bose-controlspace-controller
```

`BOSE_WEB_ADDR` changes the listen address (default `:8099`). Under the Supervisor only the ingress proxy is served.

Build the image: `docker buildx build --platform linux/arm64 --build-arg BUILD_VERSION=$(grep '^version' bose-controlspace-controller/config.yaml | cut -d'"' -f2) -t bose-controlspace-controller --load bose-controlspace-controller`.

```text
bose-controlspace-controller/
├── main.go, app.go         wiring, modes, signals; app swaps the bridge on upload
├── config.yaml, Dockerfile add-on packaging
└── internal/
    ├── design/     .csp parser → devices, blocks, parameter sets; Store keeps the uploaded file
    ├── webui/      ingress page: running design + upload
    ├── csp/        protocol: commands, response tokeniser, reconnecting client
    ├── bridge/     lifecycle · poll.go · write.go (debounce, set→ack→readback) · publish.go · manifest.go
    ├── mqtt/       connection, topics, discovery payloads, inbound router
    ├── capture/    capture mode transcripts
    ├── config/     options + Supervisor MQTT lookup
    └── testutil/   fake device (protocol emulator), fake broker
```

## Tests

`go test -race ./...` runs fully offline. The design parser is exercised against two synthetic Designer exports in `internal/design/testdata/` (a fictional venue on TEST-NET addresses; a `v1`→`v2` pair for the rename/removal drift check, with a Windows-1252 variant generated in-test); the bridge runs the whole stack against an in-memory protocol emulator; a golden file pins every discovery payload (`go test ./internal/bridge -run Golden -update` after an intentional change).

**Real design files never go in the repository.** `*.csp` and `*.cpz` are ignored everywhere; keep installation exports under the ignored `docs/` directory.
