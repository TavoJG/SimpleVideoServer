import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createMemoryHistory, createRouter } from "vue-router";
import App from "./App.vue";
import { routes } from "./routes";

const videos = [
  { id: 1, title: "At the beach", filename: "beach.jpg", relative_path: "Travel/beach.jpg", category: "Travel", subcategory: "", media_type: "image" },
  { id: 2, title: "Mountain trail", filename: "trail.jpg", relative_path: "Travel/Hikes/trail.jpg", category: "Travel", subcategory: "Hikes", media_type: "image" },
  { id: 3, title: "Family dinner", filename: "dinner.jpg", relative_path: "Private/dinner.jpg", category: "Private", subcategory: "", media_type: "image" },
  { id: 4, title: "Coast drive", filename: "drive.mp4", relative_path: "Travel/drive.mp4", category: "Travel", subcategory: "", media_type: "video" },
].map((video) => ({ ...video, tags: [], stream_url: `/media/${video.id}`, favorited: false, watch_later: false }));

const categories = [
  { name: "Travel", count: 3, direct_count: 2, locked: false, unlocked: true, subcategories: [{ name: "Hikes", count: 1 }] },
  { name: "Private", count: 1, direct_count: 1, locked: true, unlocked: false, subcategories: [] },
];

let wrapper;

beforeEach(() => {
  localStorage.clear();
  global.fetch = vi.fn(async (path) => {
    const responses = {
      "/api/auth/status": { enabled: false, authenticated: true },
      "/api/config": { default_video_root: "/library" },
      "/api/categories": categories,
      "/api/videos": videos.filter((video) => video.category !== "Private"),
      "/api/trash": [],
      "/api/classification/jobs?category=Travel": { enabled: false, jobs: [], suggestions: [] },
    };
    if (!(path in responses)) throw new Error(`Unexpected request: ${path}`);
    return { ok: true, json: async () => structuredClone(responses[path]) };
  });
});

describe('Phase 3 power tools', () => {
  it('groups tag case variants and hides unrelated folder controls on tags and trash routes', async () => {
    const router = await openApp('/category/Travel');
    wrapper.vm.videos[0].tags = ['Summer'];
    wrapper.vm.videos[1].tags = ['summer'];
    await router.push('/tags');
    await flushPromises();
    expect(wrapper.vm.tagSummaries).toEqual([{ name: 'Summer', count: 2 }]);
    await router.push('/tags/Summer');
    await flushPromises();
    expect(wrapper.find('.subcategory-browser').exists()).toBe(false);
    expect(wrapper.find('.category-management').exists()).toBe(false);
    expect(wrapper.find('.back-button').text()).toBe('Back to tags');
    await router.push('/trash');
    await flushPromises();
    expect(wrapper.find('.subcategory-browser').exists()).toBe(false);
    expect(wrapper.text()).toContain('Trash is empty.');
  });
  it('selects ranges, selects all visible, clears on filters and keeps row controls separate', async () => {
    await openApp('/category/Travel');
    expect(wrapper.find('button button').exists()).toBe(false);
    await wrapper.findAll('.row-checkbox')[0].setValue(true);
    wrapper.vm.toggleSelection(4, { shiftKey: true, target: { checked: true } });
    expect(wrapper.vm.selectedIds).toEqual([1, 4]);
    expect(wrapper.vm.allVisibleSelected).toBe(true);
    wrapper.vm.selectAllVisible(false);
    expect(wrapper.vm.selectedIds).toEqual([]);
    wrapper.vm.selectAllVisible(true);
    await wrapper.find('#search').setValue('beach');
    expect(wrapper.vm.selectedIds).toEqual([]);
    expect(wrapper.vm.filteredVideos.map(video => video.id)).toEqual([1]);
  });

  it('sends bulk move destinations and retries only failures with the same operation', async () => {
    await openApp('/category/Travel');
    const original = wrapper.vm.api.bind(wrapper.vm);
    const requests = [];
    vi.spyOn(wrapper.vm, 'api').mockImplementation((path, options) => {
      if (path !== '/api/videos/bulk') return original(path, options);
      const payload = JSON.parse(options.body);
      requests.push(payload);
      return Promise.resolve({ results: payload.ids.map(id => ({ id, success: id === 1 || requests.length > 1, error: 'File busy' })) });
    });
    wrapper.vm.selectAllVisible(true);
    wrapper.vm.openBulkDialog('move');
    wrapper.vm.powerDialog.targetCategory = 'Travel';
    wrapper.vm.powerDialog.targetSubcategory = 'Hikes';
    await wrapper.vm.submitPowerDialog();
    await flushPromises();
    expect(requests[0]).toEqual({ ids: [1, 4], action: 'move', category: 'Travel', subcategory: 'Hikes' });
    expect(wrapper.text()).toContain('File busy');
    await wrapper.vm.retryFailures();
    expect(requests[1]).toEqual({ ids: [4], action: 'move', category: 'Travel', subcategory: 'Hikes' });
    expect(wrapper.vm.powerFailures).toEqual([]);
  });

  it('supports each tag mode and explicit favorite and later values', async () => {
    await openApp('/category/Travel');
    const original = wrapper.vm.api.bind(wrapper.vm);
    const payloads = [];
    vi.spyOn(wrapper.vm, 'api').mockImplementation((path, options) => {
      if (path !== '/api/videos/bulk') return original(path, options);
      const payload = JSON.parse(options.body);
      payloads.push(payload);
      return Promise.resolve({ results: payload.ids.map(id => ({ id, success: true })) });
    });
    for (const mode of ['add', 'remove', 'replace']) {
      wrapper.vm.selectedIds = [1];
      wrapper.vm.openBulkDialog('tags');
      Object.assign(wrapper.vm.powerDialog, { tags: 'travel, family, travel', tagMode: mode });
      await wrapper.vm.submitPowerDialog();
    }
    for (const action of ['favorited', 'watch_later']) {
      wrapper.vm.selectedIds = [1];
      wrapper.vm.openBulkDialog(action);
      wrapper.vm.powerDialog.value = false;
      await wrapper.vm.submitPowerDialog();
    }
    expect(payloads.slice(0, 3).map(payload => payload.tag_mode)).toEqual(['add', 'remove', 'replace']);
    expect(payloads[0].tags).toEqual(['travel', 'family']);
    expect(payloads.slice(3).map(payload => payload.value)).toEqual([false, false]);
  });

  it('uses exact tag routes across folders and preserves the tag during playback', async () => {
    const router = await openApp('/tags/travel');
    wrapper.vm.videos[0].tags = ['travel'];
    wrapper.vm.videos[1].tags = ['travelogue'];
    wrapper.vm.videos[2].tags = ['travel'];
    await flushPromises();
    expect(wrapper.vm.filteredVideos.map(video => video.id)).toEqual([1, 4]);
    await wrapper.find('.media-open').trigger('click');
    await flushPromises();
    expect(router.currentRoute.value.name).toBe('tag-media');
    await wrapper.find('.carousel-control.next').trigger('click');
    await flushPromises();
    expect(router.currentRoute.value.params).toEqual({ tag: 'travel', id: '4' });
    await router.push('/tags');
    await flushPromises();
    expect(wrapper.find('.tag-browser').text()).toContain('travel');
  });

  it('restores to original or chosen destination and confirms purge and empty trash', async () => {
    await openApp('/trash');
    const original = wrapper.vm.api.bind(wrapper.vm);
    const payloads = [];
    vi.spyOn(wrapper.vm, 'api').mockImplementation((path, options) => {
      if (!['/api/videos/bulk', '/api/trash/empty'].includes(path)) return original(path, options);
      const payload = JSON.parse(options.body);
      payloads.push({ path, payload });
      return Promise.resolve({ results: [{ id: 1, success: true }] });
    });
    for (const action of ['restore', 'restore', 'purge', 'empty']) {
      wrapper.vm.trashVideos = [{ ...videos[0], trashed_at: '2026-10-08T12:00:00Z', trash_path: '.trash/beach.jpg' }];
      await flushPromises();
      wrapper.vm.selectedIds = [1];
      wrapper.vm.openBulkDialog(action);
      if (payloads.length === 1) Object.assign(wrapper.vm.powerDialog, { original: false, targetCategory: 'Travel', targetSubcategory: 'Hikes' });
      await flushPromises();
      if (['purge', 'empty'].includes(action)) expect(wrapper.find('[role="dialog"]').text()).toContain('cannot be undone');
      await wrapper.vm.submitPowerDialog();
    }
    expect(payloads[0].payload).toEqual({ ids: [1], action: 'restore' });
    expect(payloads[1].payload).toEqual({ ids: [1], action: 'restore', category: 'Travel', subcategory: 'Hikes' });
    expect(payloads[2].payload.action).toBe('purge');
    expect(payloads[3]).toEqual({ path: '/api/trash/empty', payload: {} });
  });

  it('renames, promotes and trashes subcategories using the folder contract', async () => {
    const router = await openApp('/category/Travel/subcategory/Hikes');
    const original = wrapper.vm.api.bind(wrapper.vm);
    const requests = [];
    vi.spyOn(wrapper.vm, 'api').mockImplementation((path, options) => {
      if (!path.startsWith('/api/subcategories/')) return original(path, options);
      requests.push({ path, payload: JSON.parse(options.body) });
      return Promise.resolve({ results: [{ id: 2, success: false, error: 'Permission denied' }], folder_retained: true });
    });
    for (const action of ['rename', 'move', 'delete']) {
      await router.push('/category/Travel/subcategory/Hikes');
      await flushPromises();
      wrapper.vm.openFolderDialog(action);
      if (action === 'rename') wrapper.vm.powerDialog.name = 'Trails';
      await wrapper.vm.submitPowerDialog();
      await flushPromises();
    }
    expect(requests[0].payload).toEqual({ category: 'Travel', subcategory: 'Hikes', name: 'Trails' });
    expect(requests[1].payload.target_subcategory).toBe('');
    expect(requests[2].path).toBe('/api/subcategories/delete');
    expect(wrapper.text()).toContain('Permission denied');
    expect(wrapper.vm.retryPayload).toEqual({ action: 'trash', ids: [2] });
  });

  it('persists compact mode and keeps busy dialogs open on Escape', async () => {
    await openApp('/category/Travel');
    await wrapper.setData({ compactMode: true });
    expect(localStorage.getItem('video-library-compact')).toBe('true');
    expect(wrapper.classes()).toContain('compact-mode');
    wrapper.vm.selectedIds = [1];
    wrapper.vm.openBulkDialog('trash');
    await flushPromises();
    await wrapper.setData({ powerBusy: true });
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    expect(wrapper.vm.powerDialog).not.toBeNull();
    await wrapper.setData({ powerBusy: false });
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    await flushPromises();
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false);
  });

  it('batches more than 1000 IDs and treats missing results as retryable failures', async () => {
    await openApp('/category/Travel');
    const original = wrapper.vm.api.bind(wrapper.vm);
    const sizes = [];
    vi.spyOn(wrapper.vm, 'api').mockImplementation((path, options) => {
      if (path !== '/api/videos/bulk') return original(path, options);
      const payload = JSON.parse(options.body);
      sizes.push(payload.ids.length);
      return Promise.resolve({ results: payload.ids.filter(id => id !== 1001).map(id => ({ id, success: true })) });
    });
    await wrapper.vm.runBulk({ ids: Array.from({ length: 1001 }, (_, index) => index + 1), action: 'favorited', value: true });
    expect(sizes).toEqual([1000, 1]);
    expect(wrapper.vm.retryPayload.ids).toEqual([1001]);
    expect(wrapper.vm.powerFailures[0].error).toContain('No result returned');
  });

  it('retains category deletion failures in the dialog and retries just failed IDs', async () => {
    await openApp('/category/Travel');
    const original = wrapper.vm.api.bind(wrapper.vm);
    const payloads = [];
    vi.spyOn(wrapper.vm, 'api').mockImplementation((path, options) => {
      if (path === '/api/categories/delete') return Promise.resolve({ deleted: 2, folder_retained: true, results: [{ id: 1, success: true }, { id: 2, success: true }, { id: 4, success: false, error: 'File busy' }] });
      if (path === '/api/videos/bulk') {
        payloads.push(JSON.parse(options.body));
        return Promise.resolve({ results: [{ id: 4, success: true }] });
      }
      return original(path, options);
    });
    wrapper.vm.requestDeleteCategory('Travel');
    await wrapper.vm.confirmDeleteCategory();
    await flushPromises();
    expect(wrapper.find('[role="dialog"]').text()).toContain('File busy');
    expect(wrapper.vm.selectedCategory).toBe('Travel');
    await wrapper.vm.retryFailures();
    await flushPromises();
    expect(payloads).toEqual([{ ids: [4], action: 'trash' }]);
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false);
  });

  it('allows a restore destination absent from categories and restores focus after cancellation', async () => {
    await openApp('/trash');
    await wrapper.setData({ trashVideos: [{ ...videos[0], category: 'Archived', trashed_at: '2026-10-08T12:00:00Z' }], selectedIds: [1] });
    const opener = wrapper.find('#search').element;
    opener.focus();
    wrapper.vm.openBulkDialog('restore');
    await flushPromises();
    expect(document.activeElement.closest('[role="dialog"]')).not.toBeNull();
    wrapper.vm.closePowerDialog();
    await flushPromises();
    expect(document.activeElement).toBe(opener);
    wrapper.vm.openBulkDialog('restore');
    Object.assign(wrapper.vm.powerDialog, { original: false, targetCategory: 'Archived' });
    const original = wrapper.vm.api.bind(wrapper.vm);
    let payload;
    vi.spyOn(wrapper.vm, 'api').mockImplementation((path, options) => {
      if (path !== '/api/videos/bulk') return original(path, options);
      payload = JSON.parse(options.body);
      return Promise.resolve({ results: [{ id: 1, success: true }] });
    });
    await wrapper.vm.submitPowerDialog();
    expect(payload.category).toBe('Archived');
  });
});

afterEach(() => {
  wrapper?.unmount();
  wrapper = null;
  vi.restoreAllMocks();
});

async function openApp(path = "/") {
  const router = createRouter({ history: createMemoryHistory(), routes });
  await router.push(path);
  await router.isReady();
  wrapper = mount(App, { attachTo: document.body, global: { plugins: [router] } });
  await flushPromises();
  return router;
}

describe("folder browsing", () => {
  it("returns focus to the selected category on the main screen", async () => {
    const router = await openApp('/category/Travel');
    await wrapper.find('.back-button').trigger('click');
    await flushPromises();
    expect(router.currentRoute.value.name).toBe('categories');
    const tile = wrapper.find('[data-category="Travel"]');
    expect(document.activeElement).toBe(tile.element);
    expect(tile.attributes('aria-current')).toBe('true');
    expect(tile.element.parentElement.classList.contains('active')).toBe(true);
  });
  it("toggles flags and controls video playback from the keyboard", async () => {
    await openApp("/category/Travel");
    wrapper.vm.selectVideo(wrapper.vm.videos.find(video => video.id === 4), false);
    await flushPromises();
    const api = wrapper.vm.api.bind(wrapper.vm);
    vi.spyOn(wrapper.vm, "api").mockImplementation((path, options) => {
      if (path === "/api/videos/4") return Promise.resolve({ ...wrapper.vm.selected, ...JSON.parse(options.body) });
      return api(path, options);
    });
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "f" }));
    await flushPromises();
    expect(wrapper.vm.selected.favorited).toBe(true);
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "w" }));
    await flushPromises();
    expect(wrapper.vm.selected.watch_later).toBe(true);
    const player = wrapper.find("video").element;
    const play = vi.spyOn(player, "play").mockResolvedValue();
    window.dispatchEvent(new KeyboardEvent("keydown", { key: " " }));
    expect(play).toHaveBeenCalledOnce();
    Object.defineProperty(player, "paused", { configurable: true, value: false });
    const pause = vi.spyOn(player, "pause").mockImplementation(() => {});
    window.dispatchEvent(new KeyboardEvent("keydown", { key: " " }));
    expect(pause).toHaveBeenCalledOnce();
  });

  it("shows scan results, failures, and thumbnail failures", async () => {
    await openApp("/category/Travel");
    const api = wrapper.vm.api.bind(wrapper.vm);
    const scan = vi.spyOn(wrapper.vm, "api").mockImplementation((path, options) => path === "/api/scan"
      ? Promise.resolve({ found: 3, added: 1, updated: 2, missing: 1, last_scan_at: "2026-10-08T12:00:00Z" }) : api(path, options));
    await wrapper.find(".scan-form").trigger("submit");
    await flushPromises();
    expect(wrapper.find('[role="status"]').text()).toContain("missing 1");
    scan.mockRejectedValueOnce(new Error("Folder unavailable"));
    await wrapper.find(".scan-form").trigger("submit");
    await flushPromises();
    expect(wrapper.text()).toContain("Scan failed: Folder unavailable");
    wrapper.vm.videos[0].thumbnail_url = "/thumb/1";
    await flushPromises();
    await wrapper.find(".thumbnail").trigger("error");
    expect(wrapper.text()).toContain("Thumbnail unavailable");
    await wrapper.setData({ configuredRoot: "", scanResult: null });
    expect(wrapper.text()).toContain("VIDEO_ROOT is not configured");
    expect(wrapper.find(".scan-button").attributes("disabled")).toBeDefined();
  });

  it("supports navigation, details and search shortcuts while ignoring typing", async () => {
    await openApp("/category/Travel/media/1");
    wrapper.vm.selectVideo(wrapper.vm.videos.find(video => video.id === 1), false);
    await flushPromises();
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight" }));
    await flushPromises();
    expect(wrapper.vm.selected.id).toBe(4);
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowLeft" }));
    await flushPromises();
    expect(wrapper.vm.selected.id).toBe(1);
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "e" }));
    await flushPromises();
    expect(document.activeElement.id).toBe("title");
    document.activeElement.dispatchEvent(new KeyboardEvent("keydown", { key: "ArrowRight", bubbles: true }));
    expect(wrapper.vm.selected.id).toBe(1);
    document.activeElement.blur();
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "/" }));
    expect(document.activeElement.id).toBe("search");
  });

  it("traps modal focus and restores the opener on Escape", async () => {
    await openApp("/category/Travel");
    const opener = wrapper.find("#search").element;
    opener.focus();
    wrapper.vm.requestRenameCategory("Travel");
    await flushPromises();
    expect(document.activeElement.id).toBe("rename-category");
    await wrapper.find("#rename-category").trigger("keydown", { key: "Tab", shiftKey: true });
    expect(document.activeElement.textContent).toContain("Rename");
    document.activeElement.dispatchEvent(new KeyboardEvent("keydown", { key: "Tab", bubbles: true }));
    expect(document.activeElement.id).toBe("rename-category");
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    await flushPromises();
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false);
    expect(document.activeElement).toBe(opener);
  });

  it("updates titles and distinguishes filtered and locked states", async () => {
    const router = await openApp("/");
    expect(document.title).toBe("Video Library");
    await router.push("/category/Travel");
    await flushPromises();
    expect(document.title).toBe("Travel - Video Library");
    await router.push("/category/Travel/subcategory/Hikes");
    await flushPromises();
    expect(document.title).toBe("Travel / Hikes - Video Library");
    await wrapper.find(".video-row").trigger("click");
    await flushPromises();
    expect(document.title).toBe("Mountain trail - Video Library");
    await wrapper.find("#search").setValue("nothing matches");
    await flushPromises();
    expect(wrapper.text()).toContain("No media matches these filters.");
    await router.push("/category/Private");
    await flushPromises();
    expect(document.title).toBe("Private - Video Library");
    expect(wrapper.text()).toContain("Unlock this category");
  });
  it("advances after video ends without saving playback", async () => {
    const router = await openApp("/category/Travel");
    wrapper.vm.videos.find((video) => video.id === 1).title = "Zebra beach";
    await wrapper.find("#sort-mode").setValue("title");
    await flushPromises();
    await wrapper.findAll(".video-row")[0].trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.params.id).toBe("4");
    const requestCount = fetch.mock.calls.length;
    await wrapper.find("video").trigger("timeupdate");
    await wrapper.find("video").trigger("pause");
    await wrapper.find("video").trigger("ended");
    await flushPromises();
    expect(router.currentRoute.value.params.id).toBe("1");
    expect(fetch.mock.calls).toHaveLength(requestCount);
  });

  it("falls back from removed playback view and sort values", async () => {
    await openApp("/category/Travel?view=recent_played&sort=last_played");
    expect(wrapper.vm.viewMode).toBe("category");
    expect(wrapper.vm.sortMode).toBe("library");
    expect(wrapper.find('option[value="last_played"]').exists()).toBe(false);
    expect(wrapper.findAll("button").some((button) => button.text() === "Played")).toBe(false);
  });

  it("shows subfolders separately from files in the parent category", async () => {
    const router = await openApp();
    await wrapper.find(".category-tile").trigger("click");
    await flushPromises();

    expect(router.currentRoute.value.name).toBe("category");
    expect(wrapper.find(".folder-entry").text()).toContain("Hikes");
    expect(wrapper.find(".video-list").text()).toContain("At the beach");
    expect(wrapper.find(".video-list").text()).not.toContain("Mountain trail");
    expect(wrapper.find(".selected-category-menu summary strong").text()).toContain("1 folders · 2 files");
  });

  it("opens a subfolder, navigates its media, and returns to the parent", async () => {
    const router = await openApp("/category/Travel");
    await wrapper.find(".folder-entry").trigger("click");
    await flushPromises();

    expect(router.currentRoute.value.name).toBe("subcategory");
    expect(wrapper.find(".video-list").text()).toContain("Mountain trail");
    expect(wrapper.find(".video-list").text()).not.toContain("At the beach");

    await wrapper.find(".video-row").trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.name).toBe("subcategory-media");
    expect(router.currentRoute.value.params.id).toBe("2");

    await wrapper.find(".back-button").trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.name).toBe("category");
    expect(wrapper.find(".folder-entry").exists()).toBe(true);
  });

  it("keeps a locked category's files hidden", async () => {
    await openApp("/category/Private");
    expect(wrapper.find(".video-row").exists()).toBe(false);
    expect(wrapper.text()).toContain("Unlock this category to view its media.");
  });

  it("filters media by type and navigates only within the visible folder", async () => {
    const router = await openApp("/category/Travel");
    await wrapper.find(".video-row").trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.params.id).toBe("1");

    await wrapper.find(".carousel-control.next").trigger("click");
    await flushPromises();
    expect(router.currentRoute.value.params.id).toBe("4");
    expect(wrapper.find(".carousel-control.next").attributes("disabled")).toBeDefined();

    await wrapper.find("#media-filter").setValue("images");
    await flushPromises();
    expect(router.currentRoute.value.name).toBe("media");
    expect(router.currentRoute.value.params.id).toBe("1");
    expect(wrapper.findAll(".video-row")).toHaveLength(1);
  });
});
