/** Invalidates both successful and failed responses after a newer playback intent. */
export function createPlaybackRequestGuard() {
  let generation = 0
  return {
    begin(): () => boolean {
      const requested = ++generation
      return () => requested === generation
    },
    invalidate(): void {
      generation += 1
    },
  }
}
