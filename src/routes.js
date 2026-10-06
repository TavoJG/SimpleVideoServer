export const routes = [
  { path: "/", name: "categories", component: { template: "<span />" } },
  { path: "/category/:category", name: "category", component: { template: "<span />" } },
  { path: "/category/:category/subcategory/:subcategory", name: "subcategory", component: { template: "<span />" } },
  { path: "/category/:category/media/:id", name: "media", component: { template: "<span />" } },
  { path: "/category/:category/subcategory/:subcategory/media/:id", name: "subcategory-media", component: { template: "<span />" } },
];
