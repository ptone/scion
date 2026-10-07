export declare const CAPTURE_KIND: string;
export declare const RUN_KIND: string;
export declare const SUBSTEPS: string[];
export declare const STATUSES: string[];
export declare const OUTCOMES: string[];
export declare const QUARANTINE_AFTER: number;
export declare const DEFAULT_ENV_MAX_AGE_MIN: number;
export declare const SHARED_ENV_BOOLEANS: readonly string[];
export declare const DEV_AUTH_OFF_MESSAGE: string;
export declare const DEV_AUTH_ON_MESSAGE: string;
export declare const LOAD_PATHS: readonly string[];
export type ProbeClass = 'off' | 'on' | 'unexplained' | 'missing';
export declare function classifyDevAuthProbes(
  api: { status: number; message: string | null; at: string } | null,
  web: { status: number; hasIdentity: boolean; at: string } | null
): { api: ProbeClass; web: ProbeClass };
export declare const HOSTED_LOG_LINE: string;
export declare const WORKSTATION_LOG_PREFIX: string;
export declare const AUTH_MODE_SOURCES: readonly string[];
export declare function checkSupport(
  support: unknown,
  prov: unknown,
  slotGeneration: string | undefined,
  batchStart?: string
): { missing: string[]; forbidden: string[] };
export declare function checkProvenance(
  prov: unknown,
  declaredDevAuth: unknown,
  slotGeneration?: string,
  batchStart?: string
): { complete: boolean; missing: string[]; contradictions: string[]; forbidden: string[] };
export declare function gradeEEnv1(
  decl: unknown,
  probes: { api: unknown; web: unknown } | null,
  b: { attributable: boolean; bound: boolean; batchStart?: string }
): { outcome: 'pass' | 'fail' | 'inconclusive'; reasons: string[] } & Record<string, unknown>;
export declare function provenanceKey(prov: unknown): string | null;
export declare const PROVENANCE_COMPARED_FIELDS: readonly string[];
export declare function provenanceEnvKey(prov: unknown): string | null;
export declare function provenanceSupportKey(prov: unknown): string | null;
export declare function preComparisonInputs(decl: unknown): {
  envPreBaseURL: string | null;
  envPreValues: Record<string, boolean | null> | null;
  envPreProvenanceKey: string | null;
  envPreSupportKey: string | null;
  envPreProcessStartTs: string | null;
};
export declare function sharedEnvValues(decl: unknown): Record<string, boolean | null>;
export declare function applyAuthCheck(
  results: EnvGate[],
  attempt: { succeeded: boolean; at: string; detail?: string }
): EnvGate[];
export interface EnvGate {
  gate: string;
  outcome: 'pass' | 'fail' | 'inconclusive';
  details: unknown;
  awaitingPost?: boolean;
  awaitingAuthCheck?: boolean;
}
export declare function evaluateEnv(
  decl: unknown,
  selfAnon401: { status: number; at: string } | null,
  release: { slotGeneration: string; baseURL: string },
  window: { batchStart: string; maxAgeMin?: number },
  probes?: { api: unknown; web: unknown } | null
): EnvGate[];
export declare function envDecision(results: EnvGate[]): {
  stop: boolean;
  fails: string[];
  missing: string[];
  authCheckPending: boolean;
  securityReport: boolean;
};
export declare function envStop(results: EnvGate[]): boolean;
export declare function evaluateEnvPost(
  post: unknown,
  run: {
    startedAt: string;
    endedAt: string;
    slotGeneration: string;
    baseURL: string;
    preBaseURL?: string;
    preValues?: Record<string, boolean | null>;
    preProvenanceKey?: string | null;
    preSupportKey?: string | null;
    preProcessStartTs?: string | null;
    testLoginUsed?: boolean;
  }
): {
  outcome: 'pass' | 'fail' | 'inconclusive';
  problems: string[];
  fails: string[];
  open: string[];
  notes: string[];
  attributable: boolean;
};
export declare function canonicalOrigin(v: unknown): string | null;
export declare function hostBinding(
  decl: unknown,
  runBaseURL: string
): { status: 'ok' | 'missing' | 'mismatch'; declared: string | null; runOrigin: string | null };
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
export declare function coverageSummary(runDir: string): {
  perState: Record<string, { complete: number; captureError: number; blocked: number }>;
  totals: { complete: number; captureError: number; blocked: number };
};
export declare function validationRecord(
  runDir: string,
  baseFile: string,
  companionFile: string,
  envPostFile: string | undefined,
  runErrs: string[],
  identity?: ValidatorIdentity
): Record<string, unknown> & {
  errors: string[];
  classification: string;
  validator: ValidatorIdentity;
};
export interface ValidatorIdentity {
  head?: string;
  suiteDigest?: string;
  suiteFileCount?: number;
  method?: string;
  clean: boolean;
  cleanProblems: string[];
  error?: string;
}
export declare function validatorIdentity(opts?: { root?: string }): ValidatorIdentity;
