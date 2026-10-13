import adapter from '@sveltejs/adapter-static';
import {vitePreprocess} from '@sveltejs/vite-plugin-svelte';
export default {preprocess:vitePreprocess(),kit:{adapter:adapter({strict:true,precompress:true}),typescript:{config:(c)=>{c.include.push('../static/lesson.js')}},prerender:{crawl:false,entries:['/','/labs/','/learn/','/teams/','/lab/','/admin/','/admin/labs/','/admin/templates/','/admin/terminal/']},csp:{mode:'hash',directives:{'default-src':['self'],'script-src':['self'],'style-src':['self'],'img-src':['self','data:'],'font-src':['self'],'connect-src':['self']}}}};
