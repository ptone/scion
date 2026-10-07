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

import { defineConfig } from '@playwright/test';

/** LOCAL NON-EVIDENCE probe self-test (synthetic DOM via setContent; no Hub). */
export default defineConfig({
  testDir: '.',
  testMatch: ['wave01-probe.selftest.pw.ts', 'wave01-loopback.selftest.pw.ts'],
  workers: 1,
  retries: 0,
  forbidOnly: true,
  reporter: 'list',
  outputDir: '../../test-results/layout-survey-wave01-selftest',
  use: {
    trace: 'off',
    video: 'off',
    screenshot: 'off',
    browserName: 'chromium',
    launchOptions: {
      ...(process.env.CHROMIUM_EXECUTABLE
        ? { executablePath: process.env.CHROMIUM_EXECUTABLE }
        : {}),
      args: ['--no-sandbox', '--disable-setuid-sandbox'],
    },
  },
  projects: [{ name: 'chromium-selftest' }],
});
