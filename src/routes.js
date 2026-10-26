export const routes = [
  { path: '/duplicates', name: 'duplicates', component: { template: '<span />' } },
  { path: "/browse", name: "browse", component: { template: "<span />" } },
  { path: "/browse/media/:id", name: "browse-media", component: { template: "<span />" } },
  { path: "/", name: "categories", component: { template: "<span />" } },
  { path: "/category/:category", name: "category", component: { template: "<span />" } },
  { path: "/category/:category/subcategory/:subcategory", name: "subcategory", component: { template: "<span />" } },
  { path: "/category/:category/media/:id", name: "media", component: { template: "<span />" } },
  { path: "/category/:category/subcategory/:subcategory/media/:id", name: "subcategory-media", component: { template: "<span />" } },
  { path: '/:pathMatch(.*)*', redirect: '/' },
];
