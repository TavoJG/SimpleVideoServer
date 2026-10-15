export const routes = [
  { path: "/browse", name: "browse", component: { template: "<span />" } },
  { path: "/browse/media/:id", name: "browse-media", component: { template: "<span />" } },
  { path: "/tags", name: "tags", component: { template: "<span />" } },
  { path: "/tags/:tag", name: "tag", component: { template: "<span />" } },
  { path: "/tags/:tag/media/:id", name: "tag-media", component: { template: "<span />" } },
  { path: "/", name: "categories", component: { template: "<span />" } },
  { path: "/category/:category", name: "category", component: { template: "<span />" } },
  { path: "/category/:category/subcategory/:subcategory", name: "subcategory", component: { template: "<span />" } },
  { path: "/category/:category/media/:id", name: "media", component: { template: "<span />" } },
  { path: "/category/:category/subcategory/:subcategory/media/:id", name: "subcategory-media", component: { template: "<span />" } },
];
