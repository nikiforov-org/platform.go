import { defineConfig } from 'vite';
import react from '@vitejs/plugin-react';

// Dev-proxy на Gateway: фронт обращается к относительным путям (/v1, /health),
// Vite форвардит их на Gateway. Это избавляет от CORS и позволяет браузеру
// отправлять HttpOnly-куки (same-origin).
export default defineConfig({
  plugins: [react()],
  server: {
    port: 5173,
    proxy: {
      '/v1': {
        target: 'http://localhost:8080',
        changeOrigin: true,
        ws: true,
      },
      '/health': {
        target: 'http://localhost:8080',
        changeOrigin: true,
      },
    },
  },
});
