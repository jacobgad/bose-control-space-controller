# Changelog

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
