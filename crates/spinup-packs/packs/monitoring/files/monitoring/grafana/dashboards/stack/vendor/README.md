# Vendored Grafana dashboards

Community dashboards from grafana.com, pinned by revision. The main loop vendors them during a
pre-seed (CLAUDE.md, shared files). `__inputs` and `__requires` are removed, and the
`${DS_PROMETHEUS}` input is replaced with the provisioned datasource UID `prometheus`.

| File | grafana.com ID | Revision | SHA-256 of the downloaded JSON |
|---|---|---|---|
| node-exporter-full.json | 1860 (Node Exporter Full) | 45 | 184c6b7409f306da75525d7772f71945b10cea23ad16b5d78c4698ea0ea51986 |
| postgresql.json | 9628 (PostgreSQL Database) | 8 | 521bac34065bcadedf255fc384550ae0528e9a74e91b4475e583b6afe9c39cc7 |
| cadvisor.json | 14282 (Cadvisor exporter) | 1 | 76a85a0b8af8ad71c2269012b79cb71195d6d348397e191033e32035c817d5cd |
