import adapter from '@sveltejs/adapter-static';

/** @type {import('@sveltejs/kit').Config} */
const config = {
  kit: {
    version: {
      name: process.env.LABCENTRAL_WEB_VERSION ?? 'dev'
    },
    adapter: adapter({
      pages: 'build',
      assets: 'build',
      precompress: true,
      strict: true
    })
  }
};

export default config;
