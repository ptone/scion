export declare const SUITE_PATHS: readonly string[];
export declare const EXTENSION_PATHS: readonly string[];
export declare const COVERED_PATHS: readonly string[];
export declare const SUITE_DIGEST_METHOD: string;
export declare function repoRoot(cwd?: string): string;
export declare function resolveCommit(root: string, rev?: string): string;
export declare function digestEntries(entries: Array<{ path: string; bytes: Buffer }>): {
  digest: string;
  manifest: string;
  fileCount: number;
};
export interface SuiteDigest {
  commit: string;
  method: string;
  paths: string[];
  digest: string;
  manifest: string;
  fileCount: number;
}
export declare function suiteDigest(opts?: {
  root?: string;
  commit?: string;
  paths?: readonly string[];
}): SuiteDigest;
export interface CaptureHostCheck {
  ok: boolean;
  head: string;
  reviewedCommit: string;
  statusPorcelain: string;
  diffBytes: number;
  problems: string[];
}
export declare function captureHostCheck(opts: {
  root?: string;
  reviewedCommit: string;
}): CaptureHostCheck;
