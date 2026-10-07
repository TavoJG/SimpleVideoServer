<template>
  <section class="classification-review">
    <div class="classification-actions">
      <button class="secondary-button" :disabled="busy || running || !enabled" @click="start">Classify media</button>
      <button v-if="running" class="secondary-button" :disabled="busy" @click="cancel">Cancel classification</button>
      <span v-if="job" role="status">{{ job.processed }} / {{ job.total }} · {{ job.status }}</span>
      <span v-else-if="!enabled">Classifier unavailable</span>
    </div>
    <p v-if="error" role="alert">{{ error }}</p>
    <template v-if="suggestions.length">
      <h3>Review destinations</h3>
      <div v-for="item in suggestions" :key="item.id" class="classification-row">
        <input v-model="item.selected" type="checkbox" :disabled="item.status !== 'pending'" :aria-label="`Approve ${item.title}`" />
        <img :src="item.thumbnail_url" alt="" loading="lazy" />
        <div class="classification-detail">
          <strong>{{ item.title }}</strong>
          <input v-model="item.subcategory" :disabled="item.status !== 'pending'" :list="listID" :aria-label="`Destination for ${item.title}`" maxlength="100" />
          <span v-if="item.subcategory && !subcategories.includes(item.subcategory)">New subcategory</span>
          <p>{{ item.reason }}</p>
          <span>{{ item.status }}</span>
          <p v-if="item.error" role="alert">{{ item.error }}</p>
        </div>
      </div>
      <datalist :id="listID"><option v-for="name in subcategories" :key="name" :value="name" /></datalist>
      <button class="save-button" :disabled="busy || !selected.length" @click="apply">Apply selected ({{ selected.length }})</button>
    </template>
  </section>
</template>

<script>
export default {
  props: { category: { type: String, required: true }, subcategories: { type: Array, default: () => [] }, api: { type: Function, required: true } },
  emits: ['applied'],
  data: () => ({ enabled: false, jobs: [], suggestions: [], busy: false, error: '', timer: null, disposed: false }),
  computed: {
    job() { return this.jobs[0]; },
    running() { return this.jobs.some(job => job.status === 'running'); },
    selected() { return this.suggestions.filter(item => item.selected && item.status === 'pending' && item.subcategory.trim()); },
    listID() { return `classification-destinations-${encodeURIComponent(this.category)}`; },
  },
  mounted() { this.refresh(); },
  beforeUnmount() { this.disposed = true; clearTimeout(this.timer); },
  methods: {
    async refresh() {
      try {
        const data = await this.api(`/api/classification/jobs?category=${encodeURIComponent(this.category)}`);
        if (this.disposed) return;
        const edits = new Map(this.suggestions.map(item => [item.id, item]));
        this.enabled = data.enabled; this.jobs = data.jobs;
        this.suggestions = data.suggestions.map(item => {
          const previous = edits.get(item.id);
          return { ...item, selected: previous?.selected || false, subcategory: previous && item.status === 'pending' ? previous.subcategory : item.subcategory };
        });
      } catch (error) { this.error = error.message; }
      if (!this.disposed && this.running) this.timer = setTimeout(() => this.refresh(), 1500);
    },
    async action(callback) {
      this.busy = true; this.error = ''; clearTimeout(this.timer);
      try { await callback(); } catch (error) { this.error = error.message; }
      finally { this.busy = false; await this.refresh(); }
    },
    start() { return this.action(() => this.api('/api/classification/jobs', { method: 'POST', body: JSON.stringify({ category: this.category }) })); },
    cancel() { return this.action(() => this.api('/api/classification/cancel', { method: 'POST', body: JSON.stringify({ id: this.job.id }) })); },
    apply() {
      return this.action(async () => {
        const data = await this.api('/api/classification/apply', { method: 'POST', body: JSON.stringify({ items: this.selected.map(item => ({ id: item.id, subcategory: item.subcategory })) }) });
        this.error = data.results.filter(item => item.error).map(item => item.error).join('; ');
        this.$emit('applied');
      });
    },
  },
};
</script>

<style scoped>
.classification-review { margin: 16px 0; border-top: 1px solid #9996; padding-top: 12px; }
.classification-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 10px; }
.classification-row { display: grid; grid-template-columns: 20px 80px minmax(0, 1fr); gap: 12px; padding: 12px 0; border-bottom: 1px solid #9996; }
.classification-row img { width: 80px; height: 64px; object-fit: contain; }
.classification-detail { min-width: 0; overflow-wrap: anywhere; }
.classification-detail input { display: block; width: 100%; max-width: 320px; box-sizing: border-box; margin: 6px 0; }
.classification-detail p { margin: 6px 0; }
</style>
