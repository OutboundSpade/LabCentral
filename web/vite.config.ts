import {defineConfig} from 'vite';
import {sveltekit} from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
export default defineConfig({plugins:[tailwindcss(),sveltekit()],server:{proxy:{'/api':'http://127.0.0.1:8080','/auth':'http://127.0.0.1:8080'}}});
