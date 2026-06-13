<template>
  <main class="app-shell">
    <div v-if="bannerMessage" class="status-banner" :class="bannerType">
      {{ bannerMessage }}
    </div>

    <div v-if="!authChecked" class="auth-screen">
      <p class="message">Loading...</p>
    </div>

    <form v-else-if="!authenticated" class="auth-screen" @submit.prevent="login">
      <div class="auth-panel">
        <h1>Video Library</h1>
        <label for="password">Password</label>
        <input id="password" v-model="password" type="password" autocomplete="current-password" autofocus />
        <button class="scan-button" type="submit" :disabled="loggingIn">
          {{ loggingIn ? "Signing in" : "Sign in" }}
        </button>
        <p v-if="authMessage" class="message">{{ authMessage }}</p>
      </div>
    </form>

    <template v-else>
      <aside class="sidebar">
        <header class="brand">
          <h1>Video Library</h1>
          <p>{{ videos.length }} indexed media files</p>
        </header>

        <form class="scan-form" @submit.prevent="scanFolder">
          <button class="scan-button" type="submit" :disabled="scanning || !configuredRoot">
            {{ scanning ? "Scanning" : "Scan library" }}
          </button>
          <p v-if="message" class="message">{{ message }}</p>
        </form>

        <label class="search-box" for="search">Search</label>
        <input id="search" v-model="query" placeholder="Title, path, or tag" />

        <nav v-if="showCategoryGrid" class="category-grid" aria-label="Categories">
          <div
            v-for="category in categorySummaries"
            :key="category.name"
            class="category-card"
            :class="{ active: selectedCategory === category.name }"
          >
            <button class="category-tile" type="button" @click="selectCategory(category.name)">
              <span>{{ category.name }}</span>
              <strong>{{ category.count }}</strong>
            </button>
            <button
              v-if="category.name !== 'Uncategorized'"
              class="category-action"
              type="button"
              title="Rename category"
              aria-label="Rename category"
              @click="requestRenameCategory(category.name)"
            >
              ✎
            </button>
          </div>
        </nav>

        <div v-else class="media-browser">
          <button class="back-button" type="button" @click="showCategories">
            Back to categories
          </button>
          <header class="media-browser-header">
            <h2>{{ selectedCategory }}</h2>
            <p>{{ filteredVideos.length }} items</p>
          </header>

          <div class="video-list" role="list">
            <button
              v-for="video in filteredVideos"
              :key="video.id"
              class="video-row"
              :class="{ active: selected && selected.id === video.id }"
              type="button"
              @click="selectVideo(video)"
            >
              <span class="thumbnail-frame" aria-hidden="true">
                <img
                  v-if="video.thumbnail_url"
                  class="thumbnail"
                  :src="video.thumbnail_url"
                  alt=""
                  loading="lazy"
                  @error="hideThumbnail"
                />
                <span class="thumbnail-type">{{ video.media_type }}</span>
              </span>
              <span class="video-row-content">
                <span class="video-title">{{ video.title }}</span>
                <span class="media-type">{{ video.media_type }}</span>
                <span class="video-path">{{ video.relative_path }}</span>
                <span v-if="video.tags.length" class="tag-line">{{ video.tags.join(", ") }}</span>
              </span>
            </button>
            <p v-if="!filteredVideos.length" class="message">No media in this category.</p>
          </div>
        </div>
      </aside>

      <section class="viewer">
        <div v-if="selected" class="player-layout">
          <div class="carousel-stage">
            <button
              class="carousel-control previous"
              type="button"
              aria-label="Previous media"
              :disabled="!hasPreviousMedia"
              @click="playPreviousMedia"
            >
              <span class="carousel-arrow">&lt;</span>
              <span>Previous</span>
            </button>
            <video
              v-if="selected.media_type === 'video'"
              class="player"
              :src="selected.stream_url"
              controls
              autoplay
              playsinline
              webkit-playsinline
              @ended="playNextMedia"
            ></video>
            <img
              v-else
              class="image-viewer"
              :src="selected.stream_url"
              :alt="selected.title"
              @load="scheduleImageAdvance"
            />
            <button
              class="carousel-control next"
              type="button"
              aria-label="Next media"
              :disabled="!hasNextMedia"
              @click="playNextMedia"
            >
              <span>Next</span>
              <span class="carousel-arrow">&gt;</span>
            </button>
            <div class="carousel-counter">
              {{ selectedPosition }} / {{ filteredVideos.length }}
            </div>
          </div>

          <form class="details-panel" @submit.prevent="saveSelected">
            <div>
              <label for="title">Title</label>
              <input id="title" v-model="editTitle" />
            </div>
            <div>
              <label for="tags">Tags</label>
              <input id="tags" v-model="editTags" placeholder="family, travel, 2024" />
            </div>
            <div>
              <label for="category">Category</label>
              <div class="category-editor">
                <select v-if="!customCategory" id="category" v-model="editCategory">
                  <option v-for="category in categories" :key="category" :value="category">
                    {{ category }}
                  </option>
                </select>
                <input
                  v-else
                  id="category"
                  v-model="editCategory"
                  placeholder="New category"
                  autocomplete="off"
                />
                <button
                  v-if="!customCategory"
                  class="icon-button"
                  type="button"
                  title="Create category"
                  aria-label="Create category"
                  @click="enableCustomCategory"
                >
                  +
                </button>
                <button
                  v-else
                  class="icon-button"
                  type="button"
                  title="Use existing category"
                  aria-label="Use existing category"
                  @click="useExistingCategory"
                >
                  ↩
                </button>
              </div>
            </div>
            <div class="meta">
              <span>{{ selected.filename }}</span>
              <span>{{ formatBytes(selected.size_bytes) }}</span>
            </div>
            <button class="delete-button" type="button" :disabled="deleting" @click="requestDeleteSelected">
              {{ deleting ? "Deleting" : "Delete" }}
            </button>
            <button class="save-button" type="submit" :disabled="saving">
              {{ saving ? "Saving" : "Save changes" }}
            </button>
          </form>
        </div>

        <div v-else class="empty-state">
          <h2>Select media</h2>
          <p>Scan the library, then choose a video or image from the list.</p>
        </div>
      </section>

      <div
        v-if="pendingDelete"
        class="dialog-backdrop"
        role="dialog"
        aria-modal="true"
        aria-labelledby="delete-dialog-title"
        @click.self="cancelDelete"
      >
        <div class="confirm-dialog">
          <h2 id="delete-dialog-title">Delete media?</h2>
          <p>
            This will permanently delete
            <strong>{{ pendingDelete.title || pendingDelete.filename }}</strong>.
          </p>
          <div class="dialog-actions">
            <button class="secondary-button" type="button" :disabled="deleting" @click="cancelDelete">
              Cancel
            </button>
            <button class="delete-button" type="button" :disabled="deleting" @click="confirmDelete">
              {{ deleting ? "Deleting" : "Delete" }}
            </button>
          </div>
        </div>
      </div>

      <div
        v-if="pendingRenameCategory"
        class="dialog-backdrop"
        role="dialog"
        aria-modal="true"
        aria-labelledby="rename-dialog-title"
        @click.self="cancelRenameCategory"
      >
        <form class="confirm-dialog" @submit.prevent="confirmRenameCategory">
          <h2 id="rename-dialog-title">Rename category</h2>
          <p>
            Rename <strong>{{ pendingRenameCategory }}</strong> and its folder.
          </p>
          <div>
            <label for="rename-category">New category name</label>
            <input id="rename-category" v-model="renameCategoryName" autocomplete="off" />
          </div>
          <div class="dialog-actions">
            <button
              class="secondary-button"
              type="button"
              :disabled="renamingCategory"
              @click="cancelRenameCategory"
            >
              Cancel
            </button>
            <button class="save-button" type="submit" :disabled="renamingCategory">
              {{ renamingCategory ? "Renaming" : "Rename" }}
            </button>
          </div>
        </form>
      </div>
    </template>
  </main>
</template>

<script>
export default {
  data() {
    return {
      videos: [],
      selected: null,
      editTitle: "",
      editTags: "",
      editCategory: "",
      customCategory: false,
      configuredRoot: "",
      authChecked: false,
      authenticated: false,
      authEnabled: false,
      password: "",
      authMessage: "",
      loggingIn: false,
      query: "",
      selectedCategory: "Uncategorized",
      message: "",
      scanning: false,
      saving: false,
      deleting: false,
      bannerMessage: "",
      bannerType: "success",
      bannerTimer: null,
      pendingDelete: null,
      pendingRenameCategory: null,
      renameCategoryName: "",
      renamingCategory: false,
      imageAdvanceTimer: null,
      imageAdvanceMs: 6000,
    };
  },
  computed: {
    showCategoryGrid() {
      return this.$route.name === "categories";
    },
    filteredVideos() {
      const query = this.query.trim().toLowerCase();
      return this.videos.filter((video) => {
        const category = video.category || "Uncategorized";
        if (category !== this.selectedCategory) return false;
        if (!query) return true;
        const haystack = [
          video.title,
          video.filename,
          video.relative_path,
          video.media_type,
          category,
          video.tags.join(" "),
        ]
          .join(" ")
          .toLowerCase();
        return haystack.includes(query);
      });
    },
    categorySummaries() {
      const counts = new Map([["Uncategorized", 0]]);
      for (const video of this.videos) {
        const category = video.category || "Uncategorized";
        counts.set(category, (counts.get(category) || 0) + 1);
      }
      return [...counts.entries()]
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([name, count]) => ({ name, count }));
    },
    categories() {
      return this.categorySummaries.map((category) => category.name);
    },
    selectedIndex() {
      if (!this.selected) return -1;
      return this.filteredVideos.findIndex((video) => video.id === this.selected.id);
    },
    selectedPosition() {
      return this.selectedIndex >= 0 ? this.selectedIndex + 1 : 0;
    },
    hasPreviousMedia() {
      return this.selectedIndex > 0;
    },
    hasNextMedia() {
      return this.selectedIndex >= 0 && this.selectedIndex < this.filteredVideos.length - 1;
    },
  },
  watch: {
    $route() {
      this.syncRouteState();
    },
    query() {
      if (this.showCategoryGrid) return;
      this.$nextTick(() => {
        if (this.selected && this.selectedIndex < 0) {
          this.selected = null;
        }
      });
    },
  },
  async mounted() {
    await this.checkAuth();
  },
  beforeUnmount() {
    this.clearImageAdvanceTimer();
  },
  methods: {
    async api(path, options = {}) {
      const response = await fetch(path, {
        headers: { "Content-Type": "application/json" },
        ...options,
      });
      const data = await response.json().catch(() => ({}));
      if (!response.ok) {
        if (response.status === 401) {
          this.authenticated = false;
        }
        throw new Error(data.error || "Request failed");
      }
      return data;
    },
    async checkAuth() {
      try {
        const status = await this.api("/api/auth/status");
        this.authEnabled = status.enabled;
        this.authenticated = status.authenticated;
        if (this.authenticated) {
          await this.loadConfig();
          await this.loadVideos();
        }
      } catch (error) {
        this.authMessage = error.message;
      } finally {
        this.authChecked = true;
      }
    },
    async login() {
      this.loggingIn = true;
      this.authMessage = "";
      try {
        const result = await this.api("/api/auth/login", {
          method: "POST",
          body: JSON.stringify({ password: this.password }),
        });
        this.authenticated = result.authenticated;
        this.password = "";
        await this.loadConfig();
        await this.loadVideos();
      } catch (error) {
        this.authMessage = error.message;
      } finally {
        this.loggingIn = false;
      }
    },
    async logout() {
      await this.api("/api/auth/logout", { method: "POST", body: JSON.stringify({}) });
      this.authenticated = false;
      this.videos = [];
      this.clearImageAdvanceTimer();
      this.selected = null;
      this.$router.push({ name: "categories" });
    },
    async loadConfig() {
      const config = await this.api("/api/config");
      this.configuredRoot = config.default_video_root || "";
      if (!this.configuredRoot) this.message = "VIDEO_ROOT is not configured.";
    },
    async loadVideos() {
      this.videos = await this.api("/api/videos");
      if (!Array.isArray(this.videos)) this.videos = [];
      this.syncRouteState();
    },
    syncRouteState() {
      if (!this.authenticated) return;
      if (this.$route.name === "categories") {
        this.clearImageAdvanceTimer();
        this.selected = null;
        return;
      }

      const category = String(this.$route.params.category || "Uncategorized");
      this.selectedCategory = category;

      if (this.$route.name !== "media") {
        this.clearImageAdvanceTimer();
        this.selected = null;
        return;
      }

      const id = Number(this.$route.params.id);
      const video = this.videos.find((item) => item.id === id);
      if (video) {
        this.selectVideo(video, false);
      } else {
        this.clearImageAdvanceTimer();
        this.selected = null;
      }
    },
    selectCategory(category) {
      this.$router.push({ name: "category", params: { category } });
    },
    showCategories() {
      this.$router.push({ name: "categories" });
    },
    selectVideo(video, updateRoute = true) {
      this.clearImageAdvanceTimer();
      this.selected = video;
      this.selectedCategory = video.category || "Uncategorized";
      this.editTitle = video.title;
      this.editTags = video.tags.join(", ");
      this.editCategory = video.category || "Uncategorized";
      this.customCategory = false;
      this.message = "";
      if (updateRoute) {
        this.$router.push({
          name: "media",
          params: { category: this.selectedCategory, id: video.id },
        });
      }
      if (video.media_type === "image") {
        this.$nextTick(() => this.scheduleImageAdvance());
      }
    },
    showBanner(message, type = "success") {
      this.bannerMessage = message;
      this.bannerType = type;
      if (this.bannerTimer) window.clearTimeout(this.bannerTimer);
      this.bannerTimer = window.setTimeout(() => {
        this.bannerMessage = "";
      }, 2600);
    },
    clearImageAdvanceTimer() {
      if (this.imageAdvanceTimer) {
        window.clearTimeout(this.imageAdvanceTimer);
        this.imageAdvanceTimer = null;
      }
    },
    scheduleImageAdvance() {
      this.clearImageAdvanceTimer();
      if (!this.selected || this.selected.media_type !== "image") return;
      const selectedId = this.selected.id;
      this.imageAdvanceTimer = window.setTimeout(() => {
        if (this.selected && this.selected.id === selectedId) {
          this.playNextMedia();
        }
      }, this.imageAdvanceMs);
    },
    playNextMedia() {
      if (!this.selected) return;
      this.clearImageAdvanceTimer();
      const next = this.selectedIndex >= 0 ? this.filteredVideos[this.selectedIndex + 1] : null;
      if (next) this.selectVideo(next);
    },
    playPreviousMedia() {
      if (!this.selected) return;
      this.clearImageAdvanceTimer();
      const previous = this.selectedIndex > 0 ? this.filteredVideos[this.selectedIndex - 1] : null;
      if (previous) this.selectVideo(previous);
    },
    hideThumbnail(event) {
      event.target.hidden = true;
    },
    enableCustomCategory() {
      this.customCategory = true;
      this.$nextTick(() => {
        const input = document.getElementById("category");
        if (input) input.focus();
      });
    },
    useExistingCategory() {
      this.customCategory = false;
      if (!this.categories.includes(this.editCategory)) {
        this.editCategory = this.categories[0] || "Uncategorized";
      }
    },
    requestRenameCategory(category) {
      if (category === "Uncategorized" || this.renamingCategory) return;
      this.pendingRenameCategory = category;
      this.renameCategoryName = category;
      this.$nextTick(() => {
        const input = document.getElementById("rename-category");
        if (input) input.focus();
      });
    },
    cancelRenameCategory() {
      if (this.renamingCategory) return;
      this.pendingRenameCategory = null;
      this.renameCategoryName = "";
    },
    async confirmRenameCategory() {
      if (!this.pendingRenameCategory) return;
      const from = this.pendingRenameCategory;
      const to = this.renameCategoryName.trim();
      this.renamingCategory = true;
      this.message = "";
      try {
        const result = await this.api("/api/categories/rename", {
          method: "POST",
          body: JSON.stringify({ from, to }),
        });
        await this.loadVideos();
        this.selectedCategory = result.to;
        if (this.selected && (this.selected.category || "Uncategorized") === from) {
          this.clearImageAdvanceTimer();
          this.selected = null;
        }
        this.$router.push({ name: "category", params: { category: result.to } });
        this.showBanner("Category renamed.");
      } catch (error) {
        this.message = error.message;
        this.showBanner(error.message, "error");
      } finally {
        this.renamingCategory = false;
        this.pendingRenameCategory = null;
        this.renameCategoryName = "";
      }
    },
    async scanFolder() {
      this.scanning = true;
      this.message = "";
      try {
        const result = await this.api("/api/scan", {
          method: "POST",
          body: JSON.stringify({}),
        });
        this.message = `Found ${result.found}; added ${result.added}; updated ${result.updated}.`;
        await this.loadVideos();
      } catch (error) {
        this.message = error.message;
      } finally {
        this.scanning = false;
      }
    },
    async saveSelected() {
      if (!this.selected) return;
      const currentCategory = this.selectedCategory;
      const currentList = [...this.filteredVideos];
      const currentIndex = currentList.findIndex((video) => video.id === this.selected.id);
      const fallback = currentList[currentIndex + 1] || currentList[currentIndex - 1] || null;
      this.saving = true;
      this.message = "";
      try {
        const updated = await this.api(`/api/videos/${this.selected.id}`, {
          method: "PATCH",
          body: JSON.stringify({
            title: this.editTitle,
            tags: this.editTags,
            category: this.editCategory,
          }),
        });
        const index = this.videos.findIndex((video) => video.id === updated.id);
        if (index >= 0) this.videos.splice(index, 1, updated);
        const updatedCategory = updated.category || "Uncategorized";
        const movedOutOfCurrentCategory = updatedCategory !== currentCategory;
        this.selectedCategory = currentCategory;
        if (movedOutOfCurrentCategory) {
          const next = fallback ? this.videos.find((video) => video.id === fallback.id) : null;
          if (next) {
            this.selectVideo(next);
          } else {
            this.clearImageAdvanceTimer();
            this.selected = null;
            this.$router.push({ name: "category", params: { category: currentCategory } });
          }
        } else {
          this.selectVideo(updated);
        }
        this.showBanner("Changes saved.");
      } catch (error) {
        this.message = error.message;
        this.showBanner(error.message, "error");
      } finally {
        this.saving = false;
      }
    },
    requestDeleteSelected() {
      if (!this.selected || this.deleting) return;
      this.pendingDelete = this.selected;
    },
    cancelDelete() {
      if (this.deleting) return;
      this.pendingDelete = null;
    },
    async confirmDelete() {
      if (!this.pendingDelete) return;

      this.deleting = true;
      this.message = "";
      try {
        const itemToDelete = this.pendingDelete;
        const currentList = [...this.filteredVideos];
        const currentIndex = currentList.findIndex((video) => video.id === itemToDelete.id);
        const fallback = currentList[currentIndex + 1] || currentList[currentIndex - 1] || null;

        await this.api(`/api/videos/${itemToDelete.id}/delete`, {
          method: "POST",
          body: JSON.stringify({}),
        });
        const deletedId = itemToDelete.id;
        this.videos = this.videos.filter((video) => video.id !== deletedId);
        const next = fallback ? this.videos.find((video) => video.id === fallback.id) : null;
        if (next) {
          this.selectVideo(next);
        } else if (this.selected && this.selected.id === deletedId) {
          this.clearImageAdvanceTimer();
          this.selected = null;
          if (this.categories.includes(this.selectedCategory)) {
            this.$router.push({ name: "category", params: { category: this.selectedCategory } });
          } else {
            this.$router.push({ name: "categories" });
          }
        }
        this.showBanner("Deleted.");
        if (!this.categories.includes(this.selectedCategory)) {
          this.selectedCategory = this.categories[0] || "Uncategorized";
          this.$router.push({ name: "categories" });
        }
      } catch (error) {
        this.message = error.message;
        this.showBanner(error.message, "error");
      } finally {
        this.deleting = false;
        this.pendingDelete = null;
      }
    },
    formatBytes(bytes) {
      if (!bytes) return "0 B";
      const units = ["B", "KB", "MB", "GB", "TB"];
      const index = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
      const value = bytes / 1024 ** index;
      return `${value.toFixed(value >= 10 || index === 0 ? 0 : 1)} ${units[index]}`;
    },
  },
};
</script>
