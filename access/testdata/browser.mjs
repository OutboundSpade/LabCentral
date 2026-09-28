import {pathToFileURL} from 'node:url';
const {chromium}=await import(pathToFileURL(process.env.LABCENTRAL_PLAYWRIGHT_MODULE).href);
const browser=await chromium.launch({headless:true,executablePath:process.env.LABCENTRAL_CHROMIUM});
try {
 const page=await browser.newPage();
 const errors=[];page.on('pageerror',e=>errors.push(String(e)));
 await page.goto(process.argv[2]);
 await page.waitForFunction(()=>document.querySelector('#status').hidden,{timeout:15000});
 await page.locator('canvas').click();
 await page.keyboard.press('a');
 await page.waitForFunction(async()=>{const r=await fetch('/probe');return (await r.json()).keyboard},{timeout:10000});
 const pixels=await page.locator('canvas').evaluate(c=>Array.from(c.getContext('2d').getImageData(0,0,1,1).data));
 if(pixels[0]!==255 || pixels[1]!==0 || pixels[2]!==0) throw Error(`Unexpected framebuffer: ${pixels}`);
 const replay=await page.request.get(process.argv[2],{maxRedirects:0});
 if(replay.status()!==403) throw Error('Launch token replay accepted');
 const origin=await page.request.get(new URL('/probe',page.url()).href,{headers:{Origin:'https://foreign.invalid'}});
 if(origin.status()!==403)throw Error('Foreign origin accepted');
 await page.request.post(new URL('/disconnect',page.url()).href);
 await page.waitForFunction(()=>document.querySelector('#status').textContent.includes('disconnected'));
 if(errors.length)throw Error(errors.join('\n'));
 console.log('noVNC authentication, framebuffer, keyboard, replay, origin and disconnect passed');
} finally {await browser.close();}
