import { createApp } from "vue";
import { createRouter, createWebHistory } from "vue-router";
import App from "./App.vue";
import "./styles.css";

const routes = [
  { path: "/", name: "categories", component: { template: "<span />" } },
  { path: "/category/:category", name: "category", component: { template: "<span />" } },
  { path: "/category/:category/media/:id", name: "media", component: { template: "<span />" } },
];

const router = createRouter({
  history: createWebHistory(),
  routes,
});

createApp(App).use(router).mount("#app");
