import { createApp } from 'vue';

import App from './App.vue';
import { redirectOnUnauthorized, router } from './router';
import './style.css';

redirectOnUnauthorized(router);
createApp(App).use(router).mount('#app');
