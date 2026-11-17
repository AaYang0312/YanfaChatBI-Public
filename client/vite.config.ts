import react from '@vitejs/plugin-react';
import path from 'path';
import { readFileSync, readdirSync } from 'node:fs';
import { defineConfig, loadEnv, type Plugin } from 'vite';
import { transformGameDemoHtml } from './src/brand/gameDemoHtml';

// https://vitejs.dev/config/
export default defineConfig(({ mode }) => {
  const gameDemo = process.env.VITE_GAME_DEMO === 'true' || loadEnv(mode, process.cwd(), 'VITE_').VITE_GAME_DEMO === 'true';
  const demoBrand: Plugin = {
    name: 'gameshelf-demo-brand',
    transformIndexHtml: transformGameDemoHtml,
    generateBundle() {
      // Retain database icons, exclude unused original product artwork.
      for (const name of readdirSync(path.resolve(__dirname, 'public'))) {
        if (!name.toLowerCase().startsWith('yanfachatbi')) {
          this.emitFile({ type: 'asset', fileName: name, source: readFileSync(path.resolve(__dirname, 'public', name)) });
        }
      }
      for (const name of ['gameshelf-logo.svg', 'gameshelf-banner.svg']) {
        this.emitFile({ type: 'asset', fileName: name, source: readFileSync(path.resolve(__dirname, 'demo-assets', name)) });
      }
      this.emitFile({ type: 'asset', fileName: 'demo-attribution.txt', source:
        'GameShelf coursework demo. Based on NeoBase and the upstream project.\n\n' +
        readFileSync(path.resolve(__dirname, '../NOTICE'), 'utf8') + '\n' +
        readFileSync(path.resolve(__dirname, '../LICENSE'), 'utf8') });
    },
  };
  return {
  plugins: [react(), ...(gameDemo ? [demoBrand] : [])],
  optimizeDeps: {
    include: [
      'rehype-raw',
      'rehype-sanitize',
      'rehype-slug',
      'rehype-autolink-headings',
      'rehype-highlight',
      'highlight.js/styles/github.css',
      'react',
      'react-dom',
      'lucide-react'
    ]
  },
  build: {
    commonjsOptions: {
      include: [/rehype-.*/, /highlight\.js/, /node_modules/],
      transformMixedEsModules: true
    },
    rollupOptions: {
      onwarn(warning, warn) {
        // Ignore "Module level directives" warnings from lucide-react
        if (warning.code === 'MODULE_LEVEL_DIRECTIVE') {
          return;
        }
        warn(warning);
      }
    }
  },
  publicDir: gameDemo ? false : 'public',
  resolve: {
    alias: {
      '@': path.resolve(__dirname, './src'),
      '@public': path.resolve(__dirname, './public')
    }
  }
};
});
