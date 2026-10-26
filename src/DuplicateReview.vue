<template>
  <section class="duplicate-review" aria-label="Duplicate review">
    <h1>Review similar media</h1>
    <p>Import the JSON report from find_similar_media.py. Compare each pair, then delete the unwanted copy. Deletion is immediate and permanent.</p>
    <input aria-label="Similarity JSON report" type="file" accept=".json,application/json" :disabled="busy" @change="importFile" />
    <p role="status">{{ message }}</p>
    <p v-if="skipped">{{ skipped }} pairs unavailable, missing, or outside your unlocked library.</p>
    <p v-if="reportErrors">The scan reported {{ reportErrors }} errors; its results are incomplete.</p>
    <template v-if="pair">
      <p>Pair {{ index + 1 }} of {{ pairs.length }} · {{ pair.score }}% hash similarity (not confidence)</p>
      <div class="duplicate-pair">
        <article v-for="item in [pair.left, pair.right]" :key="item.id">
          <h3>{{ item.title || item.filename }}</h3>
          <video v-if="item.media_type === 'video'" :key="item.id" :src="item.stream_url" controls preload="metadata" />
          <img v-else :src="item.stream_url" :alt="item.title || item.filename" />
          <p>{{ item.relative_path }} · {{ formatBytes(item.size_bytes) }}</p>
          <a :href="item.stream_url" target="_blank" rel="noopener">Open full size</a>
          <button class="delete-button" :disabled="busy" @click="deleteItem(item)">{{ busy ? 'Deleting…' : 'Permanently delete this file' }}</button>
        </article>
      </div>
      <button :disabled="busy" @click="keepBoth">Keep both</button>
      <button :disabled="busy || index === 0" @click="index--">Previous pair</button>
      <button :disabled="busy || index === pairs.length - 1" @click="index++">Next pair</button>
    </template>
    <p v-if="loaded && !pairs.length">No candidate pairs remain. Import another report to continue.</p>
  </section>
</template>

<script>
export default {
  props: { videos: { type: Array, required: true }, api: { type: Function, required: true } },
  emits: ['deleted'],
  data: () => ({ matches: [], loaded: false, index: 0, message: '', skipped: 0, reportErrors: 0, busy: false }),
  computed: {
    pairs() {
      const byPath = new Map(this.videos.filter(v => !v.missing).map(v => [v.path, v]));
      return this.matches.flatMap(m => {
        const left = byPath.get(m.left), right = byPath.get(m.right);
        return left && right && left.id !== right.id ? [{ left, right, score: m.similarity_percent }] : [];
      });
    },
    pair() { return this.pairs[Math.min(this.index, this.pairs.length - 1)]; },

  },
  methods: {
    async importFile(event) {
      const file = event.target.files?.[0];
      if (!file) return;
      this.matches = []; this.loaded = false; this.index = 0; this.skipped = 0; this.reportErrors = 0;
      try {
        if (file.size > 10 * 1024 * 1024) throw new Error('Report must be under 10 MB.');
        const report = JSON.parse(await file.text());
        if (!Array.isArray(report.matches) || report.matches.length > 50000 || report.matches.some(m =>
          !m || typeof m.left !== 'string' || typeof m.right !== 'string' ||
          !Number.isFinite(m.similarity_percent) || m.similarity_percent < 0 || m.similarity_percent > 100)) throw new Error('Invalid similarity report.');
        this.matches = report.matches; this.loaded = true;
        this.skipped = this.matches.length - this.pairs.length;
        this.reportErrors = Array.isArray(report.errors) ? report.errors.length : 0;
        this.message = `${this.pairs.length} pairs ready for review.`;
      } catch (error) { this.message = error.message; }
    },
    keepBoth() {
      if (this.index < this.pairs.length - 1) this.index++;
      else this.message = 'End of report. Both files kept.';
    },
    async deleteItem(item) {
      if (this.busy || !this.pair || ![this.pair.left.id, this.pair.right.id].includes(item.id)) return;
      this.busy = true;
      try {
        await this.api(`/api/videos/${item.id}/delete`, { method: 'POST', body: '{}' });
        // Remove every pair containing this file, including overlapping matches.
        this.matches = this.matches.filter(m => m.left !== item.path && m.right !== item.path);
        this.index = Math.min(this.index, Math.max(0, this.pairs.length - 1));
        this.message = `Permanently deleted ${item.relative_path}.`;
        this.$emit('deleted', item.id);
      } catch (error) { this.message = `Could not delete ${item.relative_path}: ${error.message}`; }
      finally { this.busy = false; }
    },
    formatBytes(value) { return `${((value || 0) / (1024 * 1024)).toFixed(2)} MB`; },
  },
};
</script>

<style scoped>
.duplicate-review { width: 100%; min-width: 0; }
h1 { margin: 1rem 0; }
.duplicate-pair { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 1.5rem; }
.duplicate-pair article { min-width: 0; padding: 1rem; border: 1px solid #777; border-radius: 8px; }
.duplicate-pair img, .duplicate-pair video { display: block; width: 100%; height: clamp(250px, 52vh, 700px); object-fit: contain; background: #111; }
.duplicate-pair p, h3 { overflow-wrap: anywhere; }
.duplicate-pair a { display: inline-block; margin-right: 1rem; }
button { margin: .5rem 1rem .5rem 0; }
@media(max-width: 700px) { .duplicate-pair { grid-template-columns: 1fr; } }
</style>
