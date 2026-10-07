// Copyright 2026 Google LLC
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

export declare const SHA256_RE: RegExp;
export declare function sha256(data: Buffer | string): string;
export declare function sha256File(file: string): string;
export declare function listTree(root: string, include?: (rel: string) => boolean): string[];
export declare function treeManifest(root: string, include?: (rel: string) => boolean): string;
export declare function treeDigest(root: string, include?: (rel: string) => boolean): string;
export declare const SUITE_DIR: string;
export declare function scenarioSuiteSha(): string;
export declare function fixtureRecipeSha(): string;
