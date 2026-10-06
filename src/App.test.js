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
  global.fetch = vi.fn(async (path) => {
    const responses = {
      "/api/auth/status": { enabled: false, authenticated: true },
      "/api/config": { default_video_root: "/library" },
      "/api/categories": categories,
      "/api/videos": videos.filter((video) => video.category !== "Private"),
    };
    if (!(path in responses)) throw new Error(`Unexpected request: ${path}`);
    return { ok: true, json: async () => structuredClone(responses[path]) };
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
  wrapper = mount(App, { global: { plugins: [router] } });
  await flushPromises();
  return router;
}

describe("folder browsing", () => {
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
