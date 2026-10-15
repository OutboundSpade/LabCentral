import assert from 'node:assert/strict';import fs from 'node:fs';
const {chromium}=await import(process.env.LABCENTRAL_PLAYWRIGHT_MODULE);
const browser=await chromium.launch({headless:true,executablePath:process.env.LABCENTRAL_CHROMIUM,args:['--no-sandbox']});
try{
 const app=process.argv[2];const context=await browser.newContext({ignoreHTTPSErrors:true,reducedMotion:'reduce',viewport:{width:1366,height:1050}});
 await context.addCookies([{name:'__Host-labcentral',value:'cookie',url:app,secure:true,httpOnly:true,sameSite:'Lax'}]);
 const page=await context.newPage();await page.goto(app+'/admin/templates/');await page.getByText('Latest',{exact:true}).waitFor();
 const articles=page.getByRole('article');assert((await articles.first().innerText()).includes('Include netstat'));assert((await articles.nth(1).innerText()).includes('2 / 2 images ready'));
 const output=process.env.LABCENTRAL_BROWSER_EVIDENCE;if(output){fs.mkdirSync(output,{recursive:true});await page.screenshot({path:output+'/templates-desktop.png',fullPage:true})}
 assert.equal(new Set(await page.locator('article time').allInnerTexts()).size,2);
 await page.setViewportSize({width:390,height:844});assert((await articles.first().boundingBox()).width>270);assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth));if(output)await page.screenshot({path:output+'/templates-mobile.png',fullPage:true});
 await articles.first().getByRole('button',{name:'Build images',exact:true}).click();await page.getByRole('dialog').waitFor();assert((await page.getByRole('dialog').innerText()).includes('bbbbbbb'));await page.getByRole('button',{name:'Cancel',exact:true}).click();
 await articles.nth(1).getByRole('button',{name:'Remove images',exact:true}).click();await page.getByRole('dialog').waitFor();if(output)await page.screenshot({path:output+'/templates-remove-mobile.png'});
 await page.getByRole('dialog').getByRole('button',{name:'Remove unused images',exact:true}).click();await page.getByText('Images removed',{exact:true}).waitFor({timeout:15000});assert.equal(await articles.count(),2);await articles.nth(1).getByRole('button',{name:'Rebuild images',exact:true}).waitFor();
 // A build failure stays on its revision and offers a retry. This fixture has no source.
 await articles.first().getByRole('button',{name:'Build images',exact:true}).click();await page.getByRole('button',{name:'Start build',exact:true}).click();await page.getByText('Build failed',{exact:true}).waitFor({timeout:15000});await articles.first().getByRole('button',{name:'Retry build',exact:true}).waitFor();
 if(output)await page.screenshot({path:output+'/templates-failure-mobile.png',fullPage:true});
 console.log('Timeline ordering, dates, latest marker, build confirmation/failure, native-removal job fixture, retained revision and mobile layout passed.');
}finally{await browser.close()}
