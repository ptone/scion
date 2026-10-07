export declare const CAPTURE_KIND: string;
export declare const RUN_KIND: string;
export declare const SUBSTEPS: string[];
export declare const STATUSES: string[];
export declare const OUTCOMES: string[];
export declare const QUARANTINE_AFTER: number;
export interface EnvGate {
  gate: string;
  outcome: 'pass' | 'fail' | 'inconclusive';
  details: unknown;
}
export declare function evaluateEnv(
  decl: unknown,
  selfAnon401: { status: number; at: string } | null,
  release: { slotGeneration: string }
): EnvGate[];
export declare function envStop(results: EnvGate[]): boolean;
export declare function validateCapture(rec: unknown): string[];
export declare function validateRun(
  runDir: string,
  baseFile: string,
  companionFile: string
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
