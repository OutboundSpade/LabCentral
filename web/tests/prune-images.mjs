// Local API fixtures establish UI behavior, not native provider acceptance.
import assert from "node:assert/strict";
import fs from "node:fs";
const { chromium } = await import(process.env.LABCENTRAL_PLAYWRIGHT_MODULE || "playwright");
const browser = await chromium.launch({headless:true,executablePath:process.env.LABCENTRAL_CHROMIUM,args:["--no-sandbox"]});
const app = process.argv[2] || "http://127.0.0.1:5173";
const evidence = process.env.LABCENTRAL_BROWSER_EVIDENCE;
if(evidence)fs.mkdirSync(evidence,{recursive:true});
try {
  for(const width of [1366,390]) {
    const context = await browser.newContext({viewport:{width,height:900},reducedMotion:"reduce"});
    const page = await context.newPage();
    const errors=[]; page.on("pageerror",e=>errors.push(e.message));
    let posts=0, retried=0;
    const created=new Date().toISOString();
    let jobs=[];
    const template={id:"revision",name:"branch-services",commit:"1234567abc",built:true,latest:true,imagesReady:2,imagesTotal:2,buildsPending:0,removing:false};
    await page.route("**/api/**",async route=>{
      const req=route.request(),path=new URL(req.url()).pathname;
      if(path==="/api/me")return route.fulfill({json:{user:{id:"admin",name:"Administrator",admin:true},csrf:"csrf",meeting:null,membership:{},teams:[],lab:{machines:[]}}});
      if(path==="/api/templates")return route.fulfill({json:{templates:[{...template,description:"Branch recovery",machines:[],stages:[],stageCount:0,practiceStageIDs:[],scenarios:[]}]}});
      if(path==="/api/admin")return route.fulfill({json:{templates:[template],jobs,teams:[],meeting:null,breaks:[],breakRuns:[],checkRuns:[],imageSummary:{images:19,unusedImages:17}}});
      assert.equal(req.headers()["x-csrf-token"],"csrf");
      if(path==="/api/admin/images/prune") {
        assert.equal(req.method(),"POST");assert.deepEqual(JSON.parse(req.postData()),{});posts++;
        jobs=[{id:"prune",target:"inventory",kind:"prune-images",status:"failed",created,needsAttention:true,retryable:true,error:"Removal unconfirmed",imageOutcomes:[{name:"old-workstation",status:"removed"},{name:"old-web",status:"unknown",message:"Removal unconfirmed. Retry cleanup."},{name:"retained-image",status:"skipped",message:"Referenced by a retained instance."}]}];
        return route.fulfill({status:202,json:jobs[0]});
      }
      if(path==="/api/admin/jobs/retry") {
        assert.deepEqual(JSON.parse(req.postData()),{id:"prune"});retried++;
        jobs=[{...jobs[0],id:"retry",status:"running",needsAttention:false,retryable:false}];
        return route.fulfill({status:202,json:jobs[0]});
      }
      throw new Error("Unexpected API request: "+path);
    });
    await page.goto(app+"/admin/templates/");
    await page.getByRole("heading",{name:"Templates",exact:true}).waitFor();
    await page.getByRole("heading",{name:"Branch services",exact:true}).waitFor();
    await page.getByText("19 images · 17 unused",{exact:true}).waitFor();
    const prune=page.getByRole("button",{name:"Remove unused images",exact:true}).first();
    assert((await prune.boundingBox()).height>=44 || width>600);
    await prune.click();
    const dialog=page.getByRole("dialog");
    await dialog.waitFor();
    assert((await dialog.innerText()).includes("17 unused images"));
    assert((await dialog.innerText()).includes("Latest revisions, open meetings, active practice and builds are kept."));
    if(evidence)await page.screenshot({animations:"disabled",path:`${evidence}/prune-confirmation-${width}.png`});
    await dialog.getByRole("button",{name:"Cancel",exact:true}).click();await dialog.waitFor({state:"hidden"});assert.equal(posts,0);
    await prune.click();await dialog.getByRole("button",{name:"Remove unused images",exact:true}).click();
    await dialog.waitFor({state:"hidden"});assert.equal(posts,1);
    await page.getByRole("button",{name:"Needs attention (1)",exact:true}).click();
    // Removed and refused images both count as processed.
    const progress=page.locator("summary").filter({hasText:"images processed"});
    assert((await progress.innerText()).includes("2 / 3"));
    await progress.click();await page.getByText("Referenced by a retained instance.",{exact:true}).waitFor();
    if(evidence)await page.screenshot({path:`${evidence}/prune-attention-${width}.png`,fullPage:true});
    await page.getByRole("button",{name:"Retry recovery",exact:true}).click();assert.equal(retried,1);
    await page.getByText("Removing unused images…",{exact:true}).waitFor();
    assert(await page.getByRole("button",{name:"Removing unused images…",exact:true}).isDisabled());
    assert(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth+1));
    assert.deepEqual(errors,[]);
    if(evidence)await page.screenshot({path:`${evidence}/prune-progress-${width}.png`,fullPage:true});
    await context.close();console.log(`PASS image cleanup confirmation, count, outcomes, retry and responsive layout (${width})`);
  }
} finally {await browser.close()}
