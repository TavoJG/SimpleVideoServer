// Queue and history live only for the current page session.
export function shuffled(ids, random = Math.random) {
  const result = [...ids];
  for (let i = result.length - 1; i > 0; i--) {
    const j = Math.floor(random() * (i + 1));
    [result[i], result[j]] = [result[j], result[i]];
  }
  return result;
}

export function createShuffle(ids, current) {
  const anchored = ids.includes(current);
  return {
    queue: shuffled(ids.filter(id => id !== current)),
    history: anchored ? [current] : [],
    position: anchored ? 0 : -1,
  };
}

export function nextShuffle(state, ids, current) {
  if (!ids.length) return null;
  if (state.position < state.history.length - 1) return state.history[++state.position];
  if (!state.queue.length) {
    if (ids.length === 1 && ids[0] === current) return null;
    state.queue = shuffled(ids);
    if (state.queue.length > 1 && state.queue[0] === current) {
      [state.queue[0], state.queue[1]] = [state.queue[1], state.queue[0]];
    }
  }
  const id = state.queue.shift();
  state.history.push(id);
  state.position = state.history.length - 1;
  return id;
}
