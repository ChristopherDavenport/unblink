import { createApp } from 'vue';
import { createRouter, createWebHistory } from 'vue-router';
import App from './App.vue';
import Home from './Home.vue';

const router = createRouter({
  history: createWebHistory(),
  routes: [
    { path: '/', component: Home },
    // Route-level code splitting: only the matched route's chunk is fetched.
    { path: '/tides/:port', component: () => import('./Tides.vue'), props: true },
  ],
});

createApp(App).use(router).mount('#app');
