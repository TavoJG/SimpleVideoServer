import { describe, expect, it } from 'vitest';
import { createShuffle, nextShuffle, shuffled } from './shuffle';

describe('shuffle queue', () => {
  it('uses Fisher–Yates without mutating input', () => {
    const ids = [1, 2, 3];
    expect(shuffled(ids, () => 0)).toEqual([2, 3, 1]);
    expect(ids).toEqual([1, 2, 3]);
  });
  it('exhausts each cycle and avoids an immediate cycle-boundary repeat', () => {
    const state = createShuffle([1, 2, 3], 1);
    const second = nextShuffle(state, [1, 2, 3], 1);
    const third = nextShuffle(state, [1, 2, 3], second);
    expect(new Set([1, second, third]).size).toBe(3);
    const fourth = nextShuffle(state, [1, 2, 3], third);
    expect(fourth).not.toBe(third);
    const rest = [nextShuffle(state, [1, 2, 3], fourth)];
    rest.push(nextShuffle(state, [1, 2, 3], rest[0]));
    expect(new Set([fourth, ...rest]).size).toBe(3);
  });
  it('follows forward history before consuming the queue', () => {
    const state = createShuffle([1, 2, 3], 1);
    const second = nextShuffle(state, [1, 2, 3], 1);
    state.position--;
    const queue = [...state.queue];
    expect(nextShuffle(state, [1, 2, 3], 1)).toBe(second);
    expect(state.queue).toEqual(queue);
  });
  it('handles empty and single-video pools without replaying', () => {
    expect(nextShuffle(createShuffle([], null), [], null)).toBeNull();
    const state = createShuffle([1], null);
    expect(nextShuffle(state, [1], null)).toBe(1);
    expect(nextShuffle(state, [1], 1)).toBeNull();
  });
});
