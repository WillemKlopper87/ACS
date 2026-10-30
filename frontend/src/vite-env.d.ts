/// <reference types="vite/client" />

interface ImportMetaEnv {
  readonly VITE_API_BASE_URL?: string;
  /** Where "Grafana ↗" points (scripts/start.sh sets it); no link when absent. */
  readonly VITE_GRAFANA_URL?: string;
}

interface ImportMeta {
  readonly env: ImportMetaEnv;
}
