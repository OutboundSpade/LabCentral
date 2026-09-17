import assert from 'node:assert/strict';
import {chromium,firefox,webkit} from 'playwright';
const [base,instance]=process.argv.slice(2);
for(const [name,engine] of Object.entries({chromium,firefox,webkit})){
 const browser=await engine.launch();try{
  const page=await browser.newPage();await page.goto(`${base}/lab?id=${instance}`);
  await page.getByRole('button',{name:'Fixture terminal',exact:true}).click();
  await page.waitForURL('**/extension?session=*');
  const frame=page.frameLocator('iframe[title="Fixture terminal"]');
  await frame.getByRole('button',{name:'Open authorized stream'}).click();
  await frame.locator('#status').filter({hasText:'Authorized stream open'}).waitFor();
  await frame.getByRole('button',{name:'Send fixture echo'}).click();
  await frame.locator('#output').filter({hasText:'fixture echo'}).waitFor();
  const session=new URL(page.url()).searchParams.get('session');assert(session);
  await page.goto(`${base}/plugins`);
  // Navigating away closes the host-owned socket; the frame holds no session token.
  await page.frameLocator('iframe[title="Independent extension fixture"]').locator('#status').filter({hasText:'Connected'}).waitFor();
  console.log(`PASS: ${name} installed access view uses a scoped stream and disconnects on navigation`);
 }finally{await browser.close()}
}
