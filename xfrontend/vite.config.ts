import { defineConfig, loadEnv } from 'vite';
import react from '@vitejs/plugin-react';

// Dev-proxy на Gateway: фронт обращается к относительным путям (/v1, /health),
// Vite форвардит их на Gateway. Это избавляет от CORS и позволяет браузеру
// отправлять HttpOnly-куки (same-origin).
export default defineConfig(({ mode }) => {
  const env = loadEnv(mode, process.cwd(), '');
  const gatewayUrl = env.VITE_GATEWAY_URL || 'http://localhost';

  return {
    plugins: [react()],
    server: {
      port: 3000,
      proxy: {
        '/v1': {
          target: gatewayUrl,
          changeOrigin: true,
          ws: true,
        },
        '/health': {
          target: gatewayUrl,
          changeOrigin: true,
        },
      },
    },
  };
});
