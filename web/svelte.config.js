import adapter from '@sveltejs/adapter-static';

/** @type {import('@sveltejs/kit').Config} */
const config = {
  kit: {
    paths: { relative: false },
    version: {
      name: process.env.LABCENTRAL_WEB_VERSION ?? 'dev'
    },
    adapter: adapter({
      pages: 'build',
      assets: 'build',
      fallback: '200.html',
      precompress: true,
      strict: true
    })
  }
};

export default config;
