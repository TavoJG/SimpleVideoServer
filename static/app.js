const { createApp } = Vue;

createApp({
  data() {
    return {
      videos: [],
      selected: null,
      editTitle: "",
      editTags: "",
      editCategory: "",
      configuredRoot: "",
      authChecked: false,
      authenticated: false,
      authEnabled: false,
      password: "",
      authMessage: "",
      loggingIn: false,
      query: "",
      categoryQuery: "",
      message: "",
      scanning: false,
      saving: false,
    };
  },
  computed: {
    filteredVideos() {
      const query = this.query.trim().toLowerCase();
      const categoryQuery = this.categoryQuery.trim().toLowerCase();
      return this.videos.filter((video) => {
        const category = video.category || "Uncategorized";
        if (categoryQuery && !category.toLowerCase().includes(categoryQuery)) {
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
    groupedVideos() {
      const groups = new Map();
      for (const video of this.filteredVideos) {
        const category = video.category || "Uncategorized";
        if (!groups.has(category)) groups.set(category, []);
        groups.get(category).push(video);
      }
      return [...groups.entries()].map(([category, videos]) => ({ category, videos }));
    },
    categories() {
      const categories = new Set(["Uncategorized"]);
      for (const video of this.videos) {
        categories.add(video.category || "Uncategorized");
      }
      return [...categories].sort((a, b) => a.localeCompare(b));
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
      if (this.selected) {
        const fresh = this.videos.find((video) => video.id === this.selected.id);
        if (fresh) this.selectVideo(fresh);
      }
    },
    selectVideo(video) {
      this.selected = video;
      this.editTitle = video.title;
      this.editTags = video.tags.join(", ");
      this.editCategory = video.category || "Uncategorized";
      this.message = "";
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
        this.selectVideo(updated);
        this.message = "Saved.";
      } catch (error) {
        this.message = error.message;
      } finally {
        this.saving = false;
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
