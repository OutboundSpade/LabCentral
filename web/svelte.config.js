import adapter from '@sveltejs/adapter-static';
import {vitePreprocess} from '@sveltejs/vite-plugin-svelte';
export default {preprocess:vitePreprocess(),kit:{adapter:adapter({strict:true,precompress:true}),prerender:{crawl:false,entries:['/','/teams/','/lab/','/admin/','/admin/templates/']},csp:{mode:'hash',directives:{'default-src':['self'],'script-src':['self'],'style-src':['self'],'img-src':['self','data:'],'font-src':['self'],'connect-src':['self']}}}};
