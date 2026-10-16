import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { flushPromises, mount } from "@vue/test-utils";
import { createMemoryHistory, createRouter } from "vue-router";
import App from "./App.vue";
import { routes } from "./routes";

const videos = [
  { id: 1, title: "At the beach", filename: "beach.jpg", relative_path: "Travel/beach.jpg", category: "Travel", subcategory: "", media_type: "image" },
  { id: 2, title: "Mountain trail", filename: "trail.jpg", relative_path: "Hikes/trail.jpg", category: "Hikes", subcategory: "", media_type: "image" },
  { id: 3, title: "Family dinner", filename: "dinner.jpg", relative_path: "Private/dinner.jpg", category: "Private", subcategory: "", media_type: "image" },
  { id: 4, title: "Coast drive", filename: "drive.mp4", relative_path: "Travel/drive.mp4", category: "Travel", subcategory: "", media_type: "video" },
].map((video) => ({ ...video, tags: [], stream_url: `/media/${video.id}`, favorited: false, watch_later: false }));

const categories = [
  { name: "Travel", count: 3, direct_count: 2, locked: false, unlocked: true, subcategories: [{ name: "Hikes", count: 1 }] },
  { name: "Hikes", count: 1, direct_count: 1, locked: false, unlocked: true },
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
      "/api/category-migrations": [{ parent: "Travel", subcategory: "Hikes", category: "Hikes" }],
      "/api/videos": videos.filter((video) => video.category !== "Private"),
    };
    if (!(path in responses)) throw new Error(`Unexpected request: ${path}`);
    return { ok: true, json: async () => structuredClone(responses[path]) };
  });
});

describe('Phase 3 power tools', () => {
  it('groups tag case variants and hides unrelated folder controls on tag routes', async () => {
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
    await wrapper.vm.submitPowerDialog();
    await flushPromises();
    expect(requests[0]).toEqual({ ids: [1, 4], action: 'move', category: 'Travel' });
    expect(wrapper.text()).toContain('File busy');
    await wrapper.vm.retryFailures();
    expect(requests[1]).toEqual({ ids: [4], action: 'move', category: 'Travel' });
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
      Object.assign(wrapper.vm.powerDialog, { tags: 'travel\nfamily\ntravel', tagMode: mode });
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

  it('confirms permanent bulk deletion and removes Trash navigation', async () => {
    await openApp('/category/Travel');
    expect(routes.some(route => route.name === 'trash')).toBe(false);
    expect(wrapper.text()).not.toContain('Trash');
    wrapper.vm.selectedIds = [1];
    wrapper.vm.openBulkDialog('delete');
    await flushPromises();
    expect(wrapper.find('[role="dialog"]').text()).toContain('cannot be undone');
    const original = wrapper.vm.api.bind(wrapper.vm);
    let payload;
    vi.spyOn(wrapper.vm, 'api').mockImplementation((path, options) => {
      if (path !== '/api/videos/bulk') return original(path, options);
      payload = JSON.parse(options.body);
      return Promise.resolve({ results: [{ id: 1, success: true }] });
    });
    await wrapper.vm.submitPowerDialog();
    expect(payload).toEqual({ ids: [1], action: 'delete' });
  });

  it('persists compact mode and keeps busy dialogs open on Escape', async () => {
    await openApp('/category/Travel');
    await wrapper.setData({ compactMode: true });
    expect(localStorage.getItem('video-library-compact')).toBe('true');
    expect(wrapper.classes()).toContain('compact-mode');
    wrapper.vm.selectedIds = [1];
    wrapper.vm.openBulkDialog('delete');
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
    expect(payloads).toEqual([{ ids: [4], action: 'delete' }]);
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false);
  });

  it('allows a move destination absent from categories and restores focus after cancellation', async () => {
    await openApp('/category/Travel');
    wrapper.vm.selectedIds = [1];
    const opener = wrapper.find('#search').element;
    opener.focus();
    wrapper.vm.openBulkDialog('move');
    await flushPromises();
    expect(document.activeElement.closest('[role="dialog"]')).not.toBeNull();
    wrapper.vm.closePowerDialog();
    await flushPromises();
    expect(document.activeElement).toBe(opener);
    wrapper.vm.openBulkDialog('move');
    wrapper.vm.powerDialog.targetCategory = 'Archived';
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

describe('combined tag browsing', () => {
  async function taggedApp(path) {
    const router = await openApp(path);
    wrapper.vm.videos[0].tags = ['Travel', 'Hiking'];
    wrapper.vm.videos[1].tags = ['travel'];
    wrapper.vm.videos[2].tags = ['travelogue', 'hiking'];
    await flushPromises();
    return router;
  }
  it('matches all or any exact case-insensitive tags across categories', async () => {
    const router = await taggedApp('/browse?tag=TRAVEL&tag=hiking');
    expect(wrapper.vm.tagMatch).toBe('all');
    expect(wrapper.vm.filteredVideos.map(video => video.id)).toEqual([1]);
    await wrapper.find('#tag-match').setValue('any');
    await flushPromises();
    expect(wrapper.vm.filteredVideos.map(video => video.id)).toEqual([1, 2, 4]);
    expect(router.currentRoute.value.query.match).toBe('any');
    expect(wrapper.vm.activeTags).toEqual(['TRAVEL', 'hiking']);
  });
  it('combines category and tags and clears selections on filter changes', async () => {
    await taggedApp('/category/Travel?tag=travel&tag=hiking');
    expect(wrapper.vm.filteredVideos.map(video => video.id)).toEqual([1]);
    wrapper.vm.selectedIds = [1];
    wrapper.vm.setTagFilters(['hiking'], 'all');
    await flushPromises();
    expect(wrapper.vm.filteredVideos.map(video => video.id)).toEqual([1, 4]);
    expect(wrapper.vm.selectedIds).toEqual([]);
  });
  it('preserves filter URLs during playback and supports removal and clear all', async () => {
    const router = await taggedApp('/browse?tag=travel&tag=hiking&match=any');
    wrapper.vm.selectVideo(wrapper.vm.filteredVideos[0]);
    await flushPromises();
    expect(router.currentRoute.value.name).toBe('browse-media');
    expect(router.currentRoute.value.query.tag).toEqual(['travel', 'hiking']);
    wrapper.vm.removeTag('HIKING');
    await flushPromises();
    expect(wrapper.vm.activeTags).toEqual(['travel']);
    wrapper.vm.setTagFilters([], 'all');
    await flushPromises();
    expect(wrapper.vm.activeTags).toEqual([]);
    expect(wrapper.vm.filteredVideos).toHaveLength(3);
  });
  it('excludes locked media and tags and shows empty results', async () => {
    await taggedApp('/browse?tag=secret');
    wrapper.vm.videos.push({ ...videos[2], tags: ['secret'] });
    await flushPromises();
    expect(wrapper.vm.filteredVideos).toEqual([]);
    expect(wrapper.vm.tagSummaries.some(tag => tag.name === 'secret')).toBe(false);
    expect(wrapper.text()).toContain('No media matches these tags.');
  });
  it('preserves comma-containing tags through metadata editing', async () => {
    await taggedApp('/category/Travel/media/1');
    wrapper.vm.selectVideo({ ...wrapper.vm.videos[0], tags: ['Art, Music', 'Travel'] }, false);
    expect(wrapper.vm.editTags).toBe('Art, Music\nTravel');
    expect(wrapper.vm.parseTagLines(wrapper.vm.editTags)).toEqual(['Art, Music', 'Travel']);
  });
});

describe('category tags', () => {
  async function categoryTaggedApp(path = '/') {
    const router = await openApp(path);
    const values = structuredClone(categories);
    values[0].tags = ['Travel', 'Outdoors'];
    values[1].tags = ['travel'];
    values[2].tags = ['Secret'];
    await wrapper.setData({ categorySummariesData: values });
    return router;
  }
  it('filters categories with all/any exact tags and category-name search', async () => {
    const router = await categoryTaggedApp('/?category_tag=TRAVEL&category_tag=outdoors');
    expect(wrapper.vm.filteredCategorySummaries.map(category => category.name)).toEqual(['Travel']);
    expect(wrapper.vm.categoryTagSummaries).toEqual([{ name: 'Outdoors', count: 1 }, { name: 'Travel', count: 2 }]);
    await wrapper.find('#category-tag-match').setValue('any');
    await flushPromises();
    expect(wrapper.vm.filteredCategorySummaries.map(category => category.name)).toEqual(['Travel', 'Hikes']);
    expect(router.currentRoute.value.query.category_match).toBe('any');
    await wrapper.find('#search').setValue('HI');
    expect(wrapper.vm.filteredCategorySummaries.map(category => category.name)).toEqual(['Hikes']);
  });
  it('keeps category filters separate from video tags and retains them on return', async () => {
    const router = await categoryTaggedApp('/?category_tag=Travel');
    wrapper.vm.selectCategory('Travel');
    await flushPromises();
    expect(wrapper.vm.filteredVideos.map(video => video.id)).toEqual([1, 4]);
    expect(wrapper.vm.activeTags).toEqual([]);
    await wrapper.vm.showCategories();
    await flushPromises();
    expect(router.currentRoute.value.query.category_tag).toBe('Travel');
    expect(wrapper.vm.filteredCategorySummaries).toHaveLength(2);
    wrapper.vm.removeCategoryTag('TRAVEL');
    await flushPromises();
    expect(wrapper.vm.activeCategoryTags).toEqual([]);
  });
  it('hides locked category tags and shows empty results', async () => {
    await categoryTaggedApp('/?category_tag=Secret');
    expect(wrapper.vm.categoryTagSummaries.some(tag => tag.name === 'Secret')).toBe(false);
    expect(wrapper.find('.category-tag-badges').exists()).toBe(false);
    expect(wrapper.text()).toContain('No categories match these filters.');
    expect(wrapper.text()).not.toContain('Secret (1 categories)');
  });
  it('edits category tags without changing video tags and preserves commas', async () => {
    await categoryTaggedApp('/category/Travel');
    wrapper.vm.videos[0].tags = ['Video tag'];
    const before = wrapper.vm.videos.map(video => [...video.tags]);
    await wrapper.find('.category-tags-edit').trigger('click');
    expect(wrapper.vm.categoryTagsDraft).toBe('Travel\nOutdoors');
    expect(document.activeElement.id).toBe('category-tags-input');
    await wrapper.find('#category-tags-input').setValue('Art, Music\nART, MUSIC\nNew');
    const api = wrapper.vm.api.bind(wrapper.vm);
    let payload;
    vi.spyOn(wrapper.vm, 'api').mockImplementation((url, options) => {
      if (url !== '/api/categories/tags') return api(url, options);
      payload = JSON.parse(options.body);
      return Promise.resolve({ category: 'Travel', tags: payload.tags });
    });
    await wrapper.vm.saveCategoryTags();
    await flushPromises();
    expect(payload).toEqual({ category: 'Travel', tags: ['Art, Music', 'New'] });
    expect(wrapper.vm.selectedCategorySummary.tags).toEqual(['Art, Music', 'New']);
    expect(wrapper.vm.videos.map(video => video.tags)).toEqual(before);
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false);
  });
  it('allows editing unlocked categories and retains failed edits', async () => {
    await categoryTaggedApp('/category/Private');
    expect(wrapper.find('.category-tags-edit').exists()).toBe(false);
    wrapper.vm.categorySummariesData.find(category => category.name === 'Private').unlocked = true;
    await flushPromises();
    await wrapper.find('.category-tags-edit').trigger('click');
    vi.spyOn(wrapper.vm, 'api').mockRejectedValueOnce(new Error('Permission denied'));
    await wrapper.vm.saveCategoryTags();
    await flushPromises();
    expect(wrapper.find('[role="alert"]').text()).toBe('Permission denied');
    expect(wrapper.find('[role="dialog"]').exists()).toBe(true);
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }));
    await flushPromises();
    expect(wrapper.find('[role="dialog"]').exists()).toBe(false);
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
      ? Promise.resolve({ found: 3, added: 1, updated: 2, missing: 1, removed_folders: 2, removed_categories: 1, cleanup_warnings: ['Folder busy'], last_scan_at: "2026-10-08T12:00:00Z" }) : api(path, options));
    await wrapper.find(".scan-form").trigger("submit");
    await flushPromises();
    expect(wrapper.text()).toContain("missing 1");
    expect(wrapper.text()).toContain("Removed 2 empty folders and 1 categories");
    expect(wrapper.text()).toContain("Cleanup warning: Folder busy");
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

  it.each([
    ['/category/Travel', [], 'categories'],
  ])('returns to the parent view when scanning removes %s', async (path, remaining, destination) => {
    const router = await openApp(path);
    const api = wrapper.vm.api.bind(wrapper.vm);
    vi.spyOn(wrapper.vm, 'api').mockImplementation((url, options) => {
      if (url === '/api/scan') return Promise.resolve({ found: 0, removed_folders: 1, removed_categories: 1, cleanup_warnings: [], last_scan_at: '2026-10-14T12:00:00Z' });
      if (url === '/api/categories') return Promise.resolve(remaining);
      if (url === '/api/videos') return Promise.resolve([]);
      return api(url, options);
    });
    await wrapper.vm.scanFolder();
    await flushPromises();
    expect(router.currentRoute.value.name).toBe(destination);
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
    expect(document.title).toBe("Hikes - Video Library");
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

  it('shows flat categories without subcategory controls', async () => {
    await openApp('/category/Travel');
    expect(wrapper.find('.subcategory-browser').exists()).toBe(false);
    expect(wrapper.find('#subcategory').exists()).toBe(false);
    expect(wrapper.find('.video-list').text()).not.toContain('Mountain trail');
    expect(wrapper.text()).not.toContain('Move into category');
  });

  it('redirects old subcategory media links using the migration mapping', async () => {
    const router = await openApp('/category/Travel/subcategory/Hikes/media/2');
    expect(router.currentRoute.value.name).toBe('media');
    expect(router.currentRoute.value.params).toEqual({ category: 'Hikes', id: '2' });
    expect(wrapper.vm.selected.id).toBe(2);
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


describe('shuffle playback', () => {
  async function prepare() {
    const router = await openApp('/category/Travel/media/4');
    wrapper.vm.videos.push(...[5, 6].map(id => ({ ...structuredClone(videos[3]), id, title: `Clip ${id}`, stream_url: `/media/${id}` })));
    await flushPromises();
    await wrapper.find('.shuffle-toggle').trigger('click');
    await flushPromises();
    return router;
  }
  it('keeps the current video, skips images, and shares keyboard and ended navigation', async () => {
    await prepare();
    expect(wrapper.vm.selected.id).toBe(4);
    expect(wrapper.find('.shuffle-toggle').attributes('aria-pressed')).toBe('true');
    expect(localStorage.getItem('video-library-shuffle')).toBe('true');
    await wrapper.find('video').trigger('ended');
    await flushPromises();
    const second = wrapper.vm.selected.id;
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowLeft' }));
    await flushPromises();
    expect(wrapper.vm.selected.id).toBe(4);
    window.dispatchEvent(new KeyboardEvent('keydown', { key: 'ArrowRight' }));
    await flushPromises();
    expect(wrapper.vm.selected.id).toBe(second);
    await wrapper.find('.carousel-control.next').trigger('click');
    await flushPromises();
    expect(new Set([4, second, wrapper.vm.selected.id]).size).toBe(3);
  });
  it('preserves the queue on sorting and resets it on selection, membership, and filters', async () => {
    await prepare();
    const queue = [...wrapper.vm.shuffleState.queue];
    await wrapper.find('#sort-mode').setValue('title');
    await flushPromises();
    expect(wrapper.vm.shuffleState.queue).toEqual(queue);
    wrapper.vm.selectVideo(wrapper.vm.videos.find(video => video.id === 5));
    await flushPromises();
    expect(wrapper.vm.shuffleState.history).toEqual([5]);
    wrapper.vm.videos = wrapper.vm.videos.filter(video => video.id !== 6);
    await flushPromises();
    expect(wrapper.vm.shuffleState.queue).not.toContain(6);
    await wrapper.find('#search').setValue('Clip 5');
    await flushPromises();
    expect(wrapper.vm.hasNextMedia).toBe(false);
    await wrapper.find('video').trigger('ended');
    await flushPromises();
    expect(wrapper.vm.selected.id).toBe(5);
    await wrapper.find('.shuffle-toggle').trigger('click');
    expect(wrapper.vm.shuffleEnabled).toBe(false);
  });
  it('respects tags, favorites, and locked categories in the video pool', async () => {
    const router = await prepare();
    wrapper.vm.videos.push({ ...structuredClone(videos[3]), id: 9, category: 'Private' });
    wrapper.vm.videos.find(video => video.id === 5).tags = ['Summer'];
    wrapper.vm.videos.find(video => video.id === 5).favorited = true;
    await router.push('/browse?tag=Summer&view=favorites');
    await flushPromises();
    expect(wrapper.vm.shuffleVideoIds).toEqual([5]);
    await router.push('/browse');
    await flushPromises();
    expect(wrapper.vm.shuffleVideoIds).not.toContain(9);
    wrapper.vm.videos = wrapper.vm.videos.filter(video => video.id !== 5);
    await flushPromises();
    expect(wrapper.vm.shuffleState.queue).not.toContain(5);
    expect(wrapper.vm.shuffleState.history).not.toContain(5);
  });
  it('starts from an image, disables empty pools, and restores the preference', async () => {
    localStorage.setItem('video-library-shuffle', 'true');
    await openApp('/category/Travel/media/4');
    expect(wrapper.vm.shuffleEnabled).toBe(true);
    expect(wrapper.vm.shuffleState.history).toEqual([4]);
    await wrapper.find('.shuffle-toggle').trigger('click');
    wrapper.vm.selectVideo(wrapper.vm.videos.find(video => video.id === 1));
    await flushPromises();
    await wrapper.find('.shuffle-toggle').trigger('click');
    await flushPromises();
    expect(wrapper.vm.selected.id).toBe(4);
    await wrapper.find('#media-filter').setValue('images');
    await flushPromises();
    expect(wrapper.find('.shuffle-toggle').attributes('disabled')).toBeDefined();
    expect(wrapper.text()).toContain('No videos match this view.');
  });
});


describe('main-page shuffle', () => {
  it('starts across unlocked categories and clears previous filters', async () => {
    const router = await openApp('/?type=images&tag=absent&view=favorites');
    wrapper.vm.videos.push(
      { ...structuredClone(videos[3]), id: 5, category: 'Hikes', stream_url: '/media/5' },
      { ...structuredClone(videos[3]), id: 9, category: 'Private', stream_url: '/media/9' },
    );
    await wrapper.find('#search').setValue('unmatched search');
    await wrapper.find('.shuffle-all').trigger('click');
    await flushPromises();
    expect(router.currentRoute.value.name).toBe('browse-media');
    expect(router.currentRoute.value.query).toEqual({ type: 'videos' });
    expect(wrapper.vm.query).toBe('');
    expect(wrapper.vm.shuffleEnabled).toBe(true);
    expect(localStorage.getItem('video-library-shuffle')).toBe('true');
    expect(wrapper.vm.shuffleVideoIds.sort()).toEqual([4, 5]);
    const first = wrapper.vm.selected.id;
    await wrapper.find('video').trigger('ended');
    await flushPromises();
    expect(wrapper.vm.selected.id).not.toBe(first);
    expect(router.currentRoute.value.name).toBe('browse-media');
    expect(wrapper.vm.shuffleState.history).toHaveLength(2);
  });
  it('disables main-page playback when only images or locked videos exist', async () => {
    await openApp('/');
    wrapper.vm.videos = [structuredClone(videos[0]), { ...structuredClone(videos[3]), category: 'Private' }];
    await flushPromises();
    expect(wrapper.find('.shuffle-all').attributes('disabled')).toBeDefined();
    expect(wrapper.text()).toContain('No unlocked videos available.');
  });
});
