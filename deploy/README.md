# Deployment assets

- [`prometheus/alerts.yml`](prometheus/alerts.yml) — Prometheus alerting rules
  over the series the server publishes on `/metrics`.
  See [`docs/operations.md`](../docs/operations.md#prometheus-metrics) for the
  endpoint, its exposure class, the label contract, and what each series means.
