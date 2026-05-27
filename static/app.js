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
  },
  async mounted() {
    await this.checkAuth();
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
      }
    },
    selectCategory(category) {
      this.selectedCategory = category;
      this.categoryView = "media";
      if (this.selected && (this.selected.category || "Uncategorized") !== category) {
        this.selected = null;
      }
    },
    showCategories() {
      this.categoryView = "categories";
      this.selected = null;
    },
    selectVideo(video) {
      this.selected = video;
      this.editTitle = video.title;
      this.editTags = video.tags.join(", ");
      this.editCategory = video.category || "Uncategorized";
      this.customCategory = false;
      this.message = "";
    },
    showBanner(message, type = "success") {
      this.bannerMessage = message;
      this.bannerType = type;
      if (this.bannerTimer) window.clearTimeout(this.bannerTimer);
      this.bannerTimer = window.setTimeout(() => {
        this.bannerMessage = "";
      }, 2600);
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
        this.selectedCategory = updated.category || "Uncategorized";
        this.categoryView = "media";
        this.selectVideo(updated);
        this.showBanner("Changes saved.");
      } catch (error) {
        this.message = error.message;
        this.showBanner(error.message, "error");
      } finally {
        this.saving = false;
      }
    },
    async deleteSelected() {
      if (!this.selected) return;
      const title = this.selected.title || this.selected.filename;
      if (!window.confirm(`Delete "${title}"?\n\nThis will permanently delete the file.`)) {
        return;
      }

      this.deleting = true;
      this.message = "";
      try {
        const currentList = [...this.filteredVideos];
        const currentIndex = currentList.findIndex((video) => video.id === this.selected.id);
        const fallback = currentList[currentIndex + 1] || currentList[currentIndex - 1] || null;

        await this.api(`/api/videos/${this.selected.id}`, {
          method: "DELETE",
        });
        const deletedId = this.selected.id;
        this.videos = this.videos.filter((video) => video.id !== deletedId);
        const next = fallback ? this.videos.find((video) => video.id === fallback.id) : null;
        if (next) {
          this.selectVideo(next);
        } else {
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
