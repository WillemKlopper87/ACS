/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_API_BASE_URL?: string;
  /** Where "Grafana ↗" points (scripts/start.sh sets it); no link when absent. */
  readonly VITE_GRAFANA_URL?: string;
  readonly VITE_GRAFANA_GATE?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
