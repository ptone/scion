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

import { beforeEach, describe, expect, it } from 'vitest';

import { MAX_RATE, MAX_TICK_MS, MIN_RATE, PlaybackClock } from './clock.js';
import { WarpFn, identityWarp } from './warp.js';

/** 10 s of viewer time mapping onto 100 s of run: a constant 10x compression. */
function tenXWarp(): WarpFn {
  return new WarpFn({
    knots: [
      { tauMs: 0, wallMs: 0, velocity: 10 },
      { tauMs: 10_000, wallMs: 100_000, velocity: 10 },
    ],
    totalTauMs: 10_000,
    minVelocity: 10,
    maxVelocity: 10,
  });
}

describe('PlaybackClock transport', () => {
  let clock: PlaybackClock;

  beforeEach(() => {
    clock = new PlaybackClock(tenXWarp());
  });

  it('starts paused at zero', () => {
    expect(clock.playing).toBe(false);
    expect(clock.tauMs).toBe(0);
    expect(clock.wallMs).toBe(0);
    expect(clock.rate).toBe(1);
  });

  it('plays, pauses and toggles', () => {
    clock.play();
    expect(clock.playing).toBe(true);
    clock.pause();
    expect(clock.playing).toBe(false);
    clock.toggle();
    expect(clock.playing).toBe(true);
    clock.toggle();
    expect(clock.playing).toBe(false);
  });

  it('does not advance while paused', () => {
    expect(clock.tick(16)).toBe(false);
    expect(clock.tauMs).toBe(0);
  });

  it('advances by real elapsed time at rate 1', () => {
    clock.play();
    expect(clock.tick(16)).toBe(true);
    expect(clock.tauMs).toBeCloseTo(16, 9);
    clock.tick(16);
    expect(clock.tauMs).toBeCloseTo(32, 9);
  });

  it('applies the rate multiplier', () => {
    clock.rate = 4;
    clock.play();
    clock.tick(10);
    expect(clock.tauMs).toBeCloseTo(40, 9);

    clock.rate = 0.5;
    clock.tick(10);
    expect(clock.tauMs).toBeCloseTo(45, 9);
  });

  it('clamps the rate to the supported range', () => {
    clock.rate = 100;
    expect(clock.rate).toBe(MAX_RATE);
    clock.rate = 0;
    expect(clock.rate).toBe(MIN_RATE);
    clock.rate = Number.NaN;
    expect(clock.rate).toBe(MIN_RATE);
  });

  it('accepts an initial rate, clamped', () => {
    expect(new PlaybackClock(tenXWarp(), { rate: 2 }).rate).toBe(2);
    expect(new PlaybackClock(tenXWarp(), { rate: 999 }).rate).toBe(MAX_RATE);
  });

  it('caps an absurd delta from a backgrounded tab', () => {
    clock.play();
    clock.tick(30_000);
    expect(clock.tauMs).toBe(MAX_TICK_MS);
    expect(clock.playing).toBe(true);
  });

  it('caps before applying the rate', () => {
    clock.rate = 2;
    clock.play();
    clock.tick(30_000);
    expect(clock.tauMs).toBe(MAX_TICK_MS * 2);
  });

  it('ignores non-positive and non-finite deltas', () => {
    clock.play();
    expect(clock.tick(0)).toBe(false);
    expect(clock.tick(-50)).toBe(false);
    expect(clock.tick(Number.NaN)).toBe(false);
    expect(clock.tauMs).toBe(0);
  });
});

describe('PlaybackClock end of run', () => {
  it('clamps at the end and auto-pauses', () => {
    const clock = new PlaybackClock(tenXWarp());
    clock.seekTau(9900);
    clock.play();
    expect(clock.tick(250)).toBe(true);
    expect(clock.tauMs).toBe(10_000);
    expect(clock.playing).toBe(false);
    expect(clock.atEnd).toBe(true);
    expect(clock.progress).toBe(1);
  });

  it('reports atEnd only once the end is reached', () => {
    const clock = new PlaybackClock(tenXWarp());
    expect(clock.atEnd).toBe(false);
    clock.seekTau(1e9);
    expect(clock.atEnd).toBe(true);
  });

  it('restarts from the beginning when play is pressed at the end', () => {
    const clock = new PlaybackClock(tenXWarp());
    clock.seekTau(10_000);
    clock.play();
    expect(clock.tauMs).toBe(0);
    expect(clock.playing).toBe(true);
  });

  it('never advances a zero-length run', () => {
    const clock = new PlaybackClock(new WarpFn(identityWarp(0)));
    expect(clock.atEnd).toBe(true);
    clock.play();
    expect(clock.tick(16)).toBe(false);
    expect(clock.tauMs).toBe(0);
    expect(clock.playing).toBe(false);
  });
});

describe('PlaybackClock seeking', () => {
  let clock: PlaybackClock;

  beforeEach(() => {
    clock = new PlaybackClock(tenXWarp());
  });

  it('seeks in viewer time, clamped', () => {
    clock.seekTau(2500);
    expect(clock.tauMs).toBe(2500);
    clock.seekTau(-100);
    expect(clock.tauMs).toBe(0);
    clock.seekTau(1e9);
    expect(clock.tauMs).toBe(10_000);
  });

  it('seeks in wall time through the warp', () => {
    clock.seekWall(50_000);
    expect(clock.tauMs).toBeCloseTo(5000, 9);
    expect(clock.wallMs).toBeCloseTo(50_000, 6);
  });

  it('round-trips a wall seek', () => {
    for (const wall of [0, 1234, 55_555, 100_000]) {
      clock.seekWall(wall);
      expect(clock.wallMs).toBeCloseTo(wall, 6);
    }
  });

  it('keeps seeking available while playing', () => {
    clock.play();
    clock.seekTau(1000);
    expect(clock.playing).toBe(true);
    expect(clock.tauMs).toBe(1000);
  });
});

describe('PlaybackClock derived values', () => {
  it('multiplies the warp velocity by the rate', () => {
    const clock = new PlaybackClock(tenXWarp());
    expect(clock.velocity).toBeCloseTo(10, 9);
    clock.rate = 2;
    expect(clock.velocity).toBeCloseTo(20, 9);
    clock.rate = 0.25;
    expect(clock.velocity).toBeCloseTo(2.5, 9);
  });

  it('tracks wall time through a non-uniform warp', () => {
    const warp = new WarpFn({
      knots: [
        { tauMs: 0, wallMs: 0, velocity: 1 },
        { tauMs: 1000, wallMs: 1000, velocity: 1 },
        { tauMs: 2000, wallMs: 101_000, velocity: 100 },
      ],
      totalTauMs: 2000,
      minVelocity: 1,
      maxVelocity: 100,
    });
    const clock = new PlaybackClock(warp);
    clock.seekTau(500);
    expect(clock.wallMs).toBeCloseTo(500, 6);
    expect(clock.velocity).toBeCloseTo(1, 6);
    clock.seekTau(1500);
    expect(clock.wallMs).toBeCloseTo(51_000, 6);
    expect(clock.velocity).toBeCloseTo(100, 6);
  });

  it('reports progress across the run', () => {
    const clock = new PlaybackClock(tenXWarp());
    clock.seekTau(2500);
    expect(clock.progress).toBeCloseTo(0.25, 9);
  });
});
