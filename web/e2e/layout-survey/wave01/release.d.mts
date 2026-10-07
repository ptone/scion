export declare const COMPANION_KIND: string;
export declare const CONTRACT_SHA256: string;
export declare const FRONTEND_BASELINE: string;
export declare const BACKEND: string;
export declare function sha256(b: Buffer | string): string;
export declare function assertValueFree(obj: unknown, where: string): void;
export declare function fontManifestDigest(): { method: string; sha256: string; fileCount: number };
export declare function validatePair(a: {
  baseBytes: Buffer;
  companion: unknown;
  expect?: {
    runnerCommit?: string;
    suiteDigest?: string;
    browserVersion?: string;
    baseURL?: string;
  };
}): string[];
export declare function validateDistinctPairs(
  a: { base: any; companion: any },
  b: { base: any; companion: any }
): string[];
export declare function buildBase(args: Record<string, string>): Record<string, unknown>;
export declare function buildCompanion(args: Record<string, string>): Record<string, unknown>;
