export declare const CAPTURE_KIND: string;
export declare const RUN_KIND: string;
export declare const SUBSTEPS: string[];
export declare const STATUSES: string[];
export declare const OUTCOMES: string[];
export declare const QUARANTINE_AFTER: number;
export declare const DEFAULT_ENV_MAX_AGE_MIN: number;
export interface EnvGate {
  gate: string;
  outcome: 'pass' | 'fail' | 'inconclusive';
  details: unknown;
  awaitingPost?: boolean;
}
export declare function evaluateEnv(
  decl: unknown,
  selfAnon401: { status: number; at: string } | null,
  release: { slotGeneration: string },
  window: { batchStart: string; maxAgeMin?: number }
): EnvGate[];
export declare function envDecision(results: EnvGate[]): {
  stop: boolean;
  fails: string[];
  missing: string[];
  securityReport: boolean;
};
export declare function envStop(results: EnvGate[]): boolean;
export declare function evaluateEnvPost(
  post: unknown,
  run: { startedAt: string; endedAt: string; slotGeneration: string }
): { outcome: 'pass' | 'fail' | 'inconclusive'; problems: string[] };
export declare function validateCapture(rec: unknown): string[];
export declare function validateRun(
  runDir: string,
  baseFile: string,
  companionFile: string,
  envPostFile?: string
): string[];
export interface Ledger {
  schemaVersion: 1;
  kind: string;
  states: Record<
    string,
    {
      consecutiveErrors: number;
      quarantined: boolean;
      lastRunId: string | null;
      errorIds: string[];
    }
  >;
  history: unknown[];
}
export declare function emptyLedger(): Ledger;
export declare function loadLedger(file: string): Ledger;
export declare function applyBatch(
  ledger: Ledger,
  runId: string,
  records: Array<{ id: string; stateId: string; status: string }>
): Ledger;
export declare function isQuarantined(ledger: Ledger, stateId: string): boolean;
export declare function clearQuarantine(
  ledger: Ledger,
  stateId: string,
  by: string,
  reason: string
): Ledger;
export declare function saveLedger(file: string, ledger: Ledger): void;
