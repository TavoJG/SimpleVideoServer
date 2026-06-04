const { createApp } = Vue;

createApp({
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
      categoryView: "categories",
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
    filteredVideos() {
      const query = this.query.trim().toLowerCase();
      return this.videos.filter((video) => {
        const category = video.category || "Uncategorized";
        if (category !== this.selectedCategory) {
          return false;
        }
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
  async mounted() {
    await this.checkAuth();
  },
  beforeUnmount() {
    this.clearImageAdvanceTimer();
  },
  watch: {
    query() {
      if (this.categoryView !== "media") return;
      this.$nextTick(() => {
        if (!this.selected || this.selectedIndex < 0) {
          this.selectFirstInCurrentList();
        }
      });
    },
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
    },
    async loadConfig() {
      const config = await this.api("/api/config");
      this.configuredRoot = config.default_video_root || "";
      if (!this.configuredRoot) this.message = "VIDEO_ROOT is not configured.";
    },
    async loadVideos() {
      this.videos = await this.api("/api/videos");
      if (!Array.isArray(this.videos)) this.videos = [];
      if (!this.categories.includes(this.selectedCategory)) {
        this.selectedCategory = this.categories[0] || "Uncategorized";
      }
      if (this.selected) {
        const fresh = this.videos.find((video) => video.id === this.selected.id);
        if (fresh) this.selectVideo(fresh);
      } else if (this.categoryView === "media") {
        this.selectFirstInCurrentList();
      }
    },
    selectCategory(category) {
      this.selectedCategory = category;
      this.categoryView = "media";
      if (this.selected && (this.selected.category || "Uncategorized") !== category) {
        this.clearImageAdvanceTimer();
        this.selected = null;
      }
      this.$nextTick(() => this.selectFirstInCurrentList());
    },
    showCategories() {
      this.categoryView = "categories";
      this.clearImageAdvanceTimer();
      this.selected = null;
    },
    selectVideo(video) {
      this.clearImageAdvanceTimer();
      this.selected = video;
      this.editTitle = video.title;
      this.editTags = video.tags.join(", ");
      this.editCategory = video.category || "Uncategorized";
      this.customCategory = false;
      this.message = "";
      if (video.media_type === "image") {
        this.$nextTick(() => this.scheduleImageAdvance());
      }
    },
    selectFirstInCurrentList() {
      const first = this.filteredVideos[0];
      if (first) {
        this.selectVideo(first);
        return;
      }
      this.clearImageAdvanceTimer();
      this.selected = null;
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
        this.categoryView = "categories";
        if (this.selected && (this.selected.category || "Uncategorized") === from) {
          this.clearImageAdvanceTimer();
          this.selected = null;
        }
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
        this.categoryView = "media";
        if (movedOutOfCurrentCategory) {
          const next = fallback ? this.videos.find((video) => video.id === fallback.id) : null;
          if (next) {
            this.selectVideo(next);
          } else {
            this.clearImageAdvanceTimer();
            this.selected = null;
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
        }
        this.showBanner("Deleted.");
        if (!this.categories.includes(this.selectedCategory)) {
          this.selectedCategory = this.categories[0] || "Uncategorized";
          this.categoryView = "categories";
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
}).mount("#app");
