<template>
  <section class="duplicate-review" aria-label="Duplicate review">
    <h2>Review similar media</h2>
    <p>Import the JSON report from find_similar_media.py. Review each pair before selecting files to delete.</p>
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
          <label><input type="checkbox" :checked="selected.includes(item.id)" :disabled="busy" @change="toggle(item.id, $event.target.checked)" /> Select for deletion</label>
        </article>
      </div>
      <button :disabled="busy" @click="keepBoth">Keep both</button>
      <button :disabled="busy || index === 0" @click="index--">Previous pair</button>
      <button :disabled="busy || index === pairs.length - 1" @click="index++">Next pair</button>
    </template>
    <button v-if="selected.length" :disabled="busy" @click="confirming = true">Review {{ selected.length }} deletions</button>
    <div v-if="confirming" role="dialog" aria-modal="true" aria-label="Confirm duplicate deletions">
      <h3>Permanently delete selected files?</h3>
      <p>This cannot be undone. Metadata will not be merged. At least one copy in each reported pair must remain.</p>
      <ul><li v-for="item in selectedItems" :key="item.id">{{ item.relative_path }}</li></ul>
      <button :disabled="busy" @click="confirming = false">Cancel</button>
      <button :disabled="busy || unsafeSelection" @click="applyDeletions">{{ busy ? 'Deleting…' : 'Permanently delete selected files' }}</button>
      <p v-if="unsafeSelection" role="alert">Both files in a pair are selected. Keep at least one.</p>
    </div>
  </section>
</template>

<script>
export default {
  props: { videos: { type: Array, required: true }, api: { type: Function, required: true } },
  emits: ['deleted'],
  data: () => ({ matches: [], selected: [], index: 0, message: '', skipped: 0, reportErrors: 0, busy: false, confirming: false }),
  computed: {
    pairs() {
      const byPath = new Map(this.videos.filter(v => !v.missing).map(v => [v.path, v]));
      return this.matches.flatMap(m => {
        const left = byPath.get(m.left), right = byPath.get(m.right);
        return left && right && left.id !== right.id ? [{ left, right, score: m.similarity_percent }] : [];
      });
    },
    pair() { return this.pairs[Math.min(this.index, this.pairs.length - 1)]; },
    selectedItems() { return this.videos.filter(v => this.selected.includes(v.id)); },
    unsafeSelection() { return this.pairs.some(p => this.selected.includes(p.left.id) && this.selected.includes(p.right.id)); },
  },
  methods: {
    async importFile(event) {
      const file = event.target.files?.[0];
      if (!file) return;
      this.matches = []; this.selected = []; this.index = 0; this.confirming = false; this.skipped = 0; this.reportErrors = 0;
      try {
        if (file.size > 10 * 1024 * 1024) throw new Error('Report must be under 10 MB.');
        const report = JSON.parse(await file.text());
        if (!Array.isArray(report.matches) || report.matches.length > 50000 || report.matches.some(m =>
          !m || typeof m.left !== 'string' || typeof m.right !== 'string' ||
          !Number.isFinite(m.similarity_percent) || m.similarity_percent < 0 || m.similarity_percent > 100)) throw new Error('Invalid similarity report.');
        this.matches = report.matches;
        this.skipped = this.matches.length - this.pairs.length;
        this.reportErrors = Array.isArray(report.errors) ? report.errors.length : 0;
        this.message = `${this.pairs.length} pairs ready for review.`;
      } catch (error) { this.message = error.message; }
    },
    toggle(id, checked) {
      this.confirming = false;
      this.selected = checked ? [...new Set([...this.selected, id])] : this.selected.filter(value => value !== id);
    },
    keepBoth() {
      this.selected = this.selected.filter(id => id !== this.pair.left.id && id !== this.pair.right.id);
      this.confirming = false;
      if (this.index < this.pairs.length - 1) this.index++;
    },
    async applyDeletions() {
      if (this.busy || !this.confirming || this.unsafeSelection || !this.selected.length) return;
      this.busy = true;
      let deleted = 0;
      try {
        // Individual API calls retain the library's authentication and category checks.
        const allowed = new Set(this.pairs.flatMap(p => [p.left.id, p.right.id]));
        for (const id of [...this.selected]) {
          if (!allowed.has(id)) throw new Error('Library changed. Import a fresh report before deleting.');
          await this.api(`/api/videos/${id}/delete`, { method: 'POST', body: '{}' });
          this.selected = this.selected.filter(value => value !== id);
          this.matches = this.matches.filter(m => {
            const item = this.videos.find(v => v.id === id);
            return m.left !== item?.path && m.right !== item?.path;
          });
          deleted++;
          this.$emit('deleted', id);
        }
        this.message = `Permanently deleted ${deleted} files.`;
      } catch (error) { this.message = `Deleted ${deleted} files. ${error.message} Review remaining selections before retrying.`; }
      finally { this.busy = false; this.confirming = false; this.index = Math.min(this.index, Math.max(0, this.pairs.length - 1)); }
    },
    formatBytes(value) { return `${((value || 0) / (1024 * 1024)).toFixed(2)} MB`; },
  },
};
</script>

<style scoped>
.duplicate-review { padding: 1rem; border: 1px solid #777; border-radius: 8px; margin: 1rem; }
.duplicate-pair { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 1rem; }
.duplicate-pair img, .duplicate-pair video { width: 100%; height: 300px; object-fit: contain; background: #111; }
.duplicate-pair p { overflow-wrap: anywhere; }
.duplicate-pair label { display: block; margin: 1rem 0; }
button { margin: .4rem; }
[role=dialog] { padding: 1rem; border: 2px solid #b44; margin-top: 1rem; }
@media(max-width: 700px) { .duplicate-pair { grid-template-columns: 1fr; } }
</style>
