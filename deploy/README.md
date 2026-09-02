# Deployment assets

- [`prometheus/alerts.yml`](prometheus/alerts.yml) — Prometheus alerting rules
  over the series the server publishes on `/metrics`.
  See [`docs/operations.md`](../docs/operations.md#prometheus-metrics) for the
  endpoint, its exposure class, the label contract, and what each series means.
- [`systemd/parameter-store.service`](systemd/parameter-store.service) — the
  unit to install at `/etc/systemd/system/parameter-store.service`; it runs
  `serve` under a dedicated user and wires `systemctl reload` to `SIGHUP` (see
  [`docs/operations.md`](../docs/operations.md#running-under-systemd)). The
  same unit is quoted in `docs/operations.md`, and a test
  (`internal/cli/deploy_unit_test.go`) fails if the two ever disagree.
