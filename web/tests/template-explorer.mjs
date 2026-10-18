// Local browser fixtures only: no platform provisioning, OIDC or guest execution.
import assert from "node:assert/strict";
import fs from "node:fs";
const { chromium } = await import(process.env.LABCENTRAL_PLAYWRIGHT_MODULE || "playwright");
const browser = await chromium.launch({headless:true, executablePath:process.env.LABCENTRAL_CHROMIUM, args:["--no-sandbox"]});
const app = process.argv[2] || "http://127.0.0.1:5173";
const evidence = process.env.LABCENTRAL_BROWSER_EVIDENCE;
if(evidence) fs.mkdirSync(evidence,{recursive:true});
const fits = p => p.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1);
const shot = async (page, name, width) => { if(evidence) await page.screenshot({path:`${evidence}/${name}-${width}.png`,fullPage:true}); };
try {
  for (const width of [1366, 1024, 390, 360]) {
    const context = await browser.newContext({viewport:{width,height:width > 600 ? 768 : 844},reducedMotion:"reduce"});
    const page = await context.newPage(), errors = [], writes = [];
    page.on("pageerror", e=>errors.push(e.message));
    const now=Date.now(), iso=n=>new Date(now+n).toISOString();
    await page.clock.install({time:now});
    let admin=false, user="student", signedIn=true, practiceFailure=false, stageOnly=false, meetingActive=true;
    let activeRun=null, meFailure=false, logouts=0, emptyCatalog=false;
    let heldDetail=null, heldSolution=null, heldAttempt=null, olderRemoving=false, olderBuilt=true;
    const plan=[], committed=new Map();
    const stages=[
      {id:"discover",title:"Map the branch",instructions:"## Investigate\n\nList the machines with `hostname`.\n\n<script>window.unsafe=1</script>",kind:"answer",prompt:"How many machines?",answer:"4",solution:"The branch has **four machines**.",break:"",checkID:""},
      {id:"verify",title:"Verify the website",instructions:"Check the bank homepage from the workstation.",kind:"action",prompt:"Keep the service reachable.",answer:"",solution:"Test `curl http://web`.",break:"",checkID:"website"},
      {id:"restore",title:"Restore the listener",instructions:"Investigate the Apache listener and restore service.",kind:"action",prompt:"Restore port 80.",answer:"",solution:"Set the listener to **80** and restart Apache.",break:"port",checkID:"website"},
      {id:"review",title:"Group review",instructions:"Discuss what changed.",kind:"instructor",prompt:"Explain the evidence.",answer:"",solution:"Review listener configuration.",break:"",checkID:""},
    ];
    const safe=s=>({id:s.id,title:s.title,instructions:s.instructions,kind:s.kind,prompt:s.prompt,hasSolution:!!s.solution});
    const machines=[{name:"workstation",cpu:2,memory:"4Gi",disk:"20Gi",access:["desktop","ssh"]},{name:"web-server-with-a-long-descriptive-machine-name-for-wrapping",cpu:2,memory:"2Gi",disk:"20Gi",access:["ssh"]}];
    const outage={id:"outage",title:"Branch outage",description:"Several branch services fail during one shift.",difficulty:"advanced",estimatedMinutes:60,variantCount:6,minBreaks:2,maxBreaks:3};
    const newest={desktopMachine:"workstation",id:"latest",name:"branch-services",commit:"1234567890abc",commitTitle:"Add template-owned recovery stages",committedAt:iso(-10000),description:"Investigate a bank's service environment and practise recovery.",handover:"# Example Branch\nKeep customer banking available.",stageCount:4,stages:stages.map(safe),machines,imagesBuilt:true,practiceStageIDs:["discover","verify"],library:{category:"Incident response",difficulty:"intermediate",estimatedMinutes:90,tags:["ccdc","web"]},scenarios:[outage]};
    const older={...newest,id:"older",commit:"abc123456789",commitTitle:"Initial branch environment",committedAt:iso(-86400000),stages:[safe(stages[0])],stageCount:1,practiceStageIDs:["discover"],scenarios:[]};
    const second={...newest,id:"network",name:"network-investigation",description:"Map a private lab network.",stages:[],stageCount:0,machines:[machines[0]],imagesBuilt:false,practiceStageIDs:[],library:{category:"Networking",difficulty:"beginner",estimatedMinutes:30,tags:["nmap"]},scenarios:[]};
    const ubuntu={...newest,id:"ubuntu",name:"ubuntu-basics",description:"Learn service management on one server.",stages:[safe(stages[0])],stageCount:1,machines:[machines[0]],imagesBuilt:false,practiceStageIDs:["discover"],library:{category:"Linux",difficulty:"beginner",estimatedMinutes:45,tags:["systemd"]},scenarios:[{...outage,id:"svc",title:"Service sweep",variantCount:3,minBreaks:1,maxBreaks:1}]};
    const dns={...newest,id:"dns",name:"dns-recovery",description:"Recover name resolution for a small office.",stages:[safe(stages[0])],stageCount:1,machines:[machines[1]],imagesBuilt:true,practiceStageIDs:["discover"],library:{category:"DNS",difficulty:"advanced",estimatedMinutes:40,tags:["bind"]},scenarios:[{...outage,id:"resolver",title:"Resolver failure"}]};
    const coach={...newest,id:"coach",name:"coach-support",coachOnly:true,description:"Coach preview environment.",library:undefined,scenarios:[{...outage,id:"coach-scenario",title:"Coach drill"}]};
    const catalog=()=>emptyCatalog?[]:admin?[newest,second,older,ubuntu,dns,coach]:[newest,second,older,ubuntu,dns];
    const privateStages=stages.map(s=>({...s,completion:s.kind==="answer"?{answer:{accepted:[s.answer,"four"],caseSensitive:false}}:s.kind==="action"?{check:s.checkID}:{instructor:true}}));
    const template={id:"latest",name:newest.name,commit:newest.commit,commitTitle:newest.commitTitle,committedAt:newest.committedAt,built:true,latest:true,imagesReady:2,imagesTotal:2,buildsPending:0,removing:false,handover:newest.handover,instructor:{notes:"## Prepare the exercise\n\n1. Select ready teams.\n2. Apply once.\n\n<script>window.unsafeNotes=1</script>",stages:privateStages,checks:[{id:"website",name:"Website",points:10}],breaks:[{id:"port",name:"Change Apache port",description:"Change the listener.",walkthrough:"### Inspect the listener\n\nRun on **web**:\n\n```sh\nsudo journalctl -u ssh --utc --no-pager\nsudo xxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxxx\n```"},{id:"stop",name:"Stop the web service",description:"Stop Apache for recovery practice.",walkthrough:"Restart Apache and check service health."}]}};
    const teams=[{id:"t1",meetingID:"meeting",name:"Team 1",capacity:6,members:2,status:"ready"},{id:"t2",meetingID:"meeting",name:"Team 2",capacity:6,members:2,status:"ready"}];
    let meeting={id:"meeting",name:"Bank recovery meeting",template:"latest",status:"ready",created:iso(0),expires:iso(7200000)};
    const quota={limitMinutes:180,usedSeconds:0,remainingSeconds:10800,resetsAt:iso(86400000)};
    const legacyLab={id:"practice",template:"latest",title:"Branch discovery",description:"Discover and verify the branch.",mode:"self-paced",estimatedMinutes:30,publishedAt:iso(-500000),stepCount:2,completedSteps:0};
    const labs=[legacyLab,
      {id:"a-base",template:"latest",title:"branch-services · Baseline practice",description:"",mode:"self-paced",estimatedMinutes:90,publishedAt:iso(-300000),stepCount:2,completedSteps:1,attempt:true},
      {id:"a-outage",template:"latest",title:"branch-services · Branch outage",description:"",mode:"self-paced",estimatedMinutes:60,publishedAt:iso(-200000),stepCount:2,completedSteps:0,attempt:true,scenarioID:"outage"},
      {id:"a-older",template:"older",title:"branch-services · Baseline practice",description:"",mode:"self-paced",estimatedMinutes:90,publishedAt:iso(-100000),stepCount:1,completedSteps:0,attempt:true}];
    const templates={latest:newest,older,network:second,ubuntu,dns,coach};
    const view=lab=>{const t=templates[lab.template], scenario=lab.scenarioID?t.scenarios.find(s=>s.id===lab.scenarioID):undefined;
      return {lab,steps:stages.slice(0,lab.stepCount).map(safe),progress:{labID:lab.id,completedSteps:lab.completedSteps,updated:iso(0)},quota,run:activeRun,releasedSteps:lab.stepCount,cloudReady:t.imagesBuilt,localAvailable:!scenario,check:null,...(scenario?{scenario}:{})};};
    const overview=()=>({user:{id:user,name:"Fixture Student",admin,email:""},csrf:`csrf-${user}`,meeting:admin&&meetingActive?meeting:null,teams:admin&&meetingActive?teams:[],membership:{userID:user,teamID:"",pendingTeam:"",switching:false},lab:{team:null,machines:[],handover:"",teammates:[],desktopActive:false}});
    await context.route("**/auth/logout",async route=> {
      assert.equal(route.request().method(),"POST");
      assert.equal(route.request().headers()["x-csrf-token"],`csrf-${user}`);
      logouts++; signedIn=false;
      return route.fulfill({json:{ok:true}});
    });
    await context.route("**/api/**",async route=> {
      const req=route.request(), path=new URL(req.url()).pathname, body=req.postData()?JSON.parse(req.postData()):null;
      if(path==="/api/me") return route.fulfill(meFailure?{status:503,json:{error:"Unavailable"}}:signedIn?{json:overview()}:{status:401,json:{error:"Sign in"}});
      if(req.method()==="POST") {assert.equal(req.headers()["x-csrf-token"],`csrf-${user}`);writes.push({path,body,user});}
      const attempt=path.match(/^\/api\/templates\/([\w-]+)\/attempt$/);
      if(attempt) {
        assert.equal(req.method(),"POST");
        assert.match(body.requestID,/^[0-9a-f]{24}$/,"request IDs are 24 lowercase hexadecimal characters");
        assert.deepEqual(Object.keys(body).toSorted(),body.scenarioID?["requestID","scenarioID"]:["requestID"],"baseline requests omit scenarioID");
        const t=templates[attempt[1]], step=plan.shift();
        if(step==="abort") return route.abort("connectionreset");
        if(step==="reject") return route.fulfill({status:409,json:{error:"Stop your active cloud workspace before starting another scenario."}});
        if(step==="hold") {const gate=heldAttempt;heldAttempt=null;await gate;}
        const key=`${user}:${body.requestID}`;
        if(committed.has(key)) return route.fulfill({status:200,json:committed.get(key)});
        const scenario=body.scenarioID?t.scenarios.find(s=>s.id===body.scenarioID):null;
        const lab={id:`attempt-${committed.size+1}`,template:t.id,title:`${t.name} · ${scenario?scenario.title:"Baseline practice"}`,description:"",mode:"self-paced",estimatedMinutes:scenario?.estimatedMinutes??90,publishedAt:iso(committed.size*1000),stepCount:2,completedSteps:0,attempt:true,...(scenario?{scenarioID:scenario.id}:{})};
        committed.set(key,lab);labs.push(lab);
        if(step==="lost") return route.fulfill({status:503,json:{error:"Service unavailable"}});
        return route.fulfill({status:201,json:lab});
      }
      if(path==="/api/templates") return route.fulfill({json:{templates:catalog()}});
      if(path.startsWith("/api/templates/")) {if(heldDetail) {const gate=heldDetail;heldDetail=null;await gate;} const t=catalog().find(t=>t.id===path.split("/").at(-1)); return route.fulfill(t?{json:t}:{status:404,json:{error:"This template revision is unavailable."}});}
      if(path==="/api/learning") return route.fulfill(practiceFailure?{status:503,json:{error:"Practice unavailable"}}:{json:{labs,quota,run:activeRun}});
      if(path==="/api/learning/practice/solution") {if(heldSolution){const gate=heldSolution;heldSolution=null;await gate;} return route.fulfill({json:{stepID:body.stepID,solution:stages.find(s=>s.id===body.stepID).solution}});}
      const learning=path.match(/^\/api\/learning\/([\w-]+)$/);
      if(learning) {const lab=labs.find(l=>l.id===learning[1]); return route.fulfill(lab?{json:view(lab)}:{status:404,json:{error:"Lab not found"}});}
      if(path==="/api/admin") return route.fulfill({json:{meeting:meetingActive?meeting:null,teams:meetingActive?teams:[],templates:emptyCatalog?[]:[stageOnly?{...template,instructor:{...template.instructor,stages:[privateStages[0]],breaks:[]}}:template,{...template,id:"network",name:second.name,built:false,imagesReady:0},{...template,id:"older",commit:older.commit,latest:false,committedAt:older.committedAt,built:olderBuilt,removing:olderRemoving}],breaks:stageOnly?[]:template.instructor.breaks,breakRuns:[],checkRuns:[],jobs:[],joinedStudents:4,teamDesktops:[],activeDesktops:0,repositoryURL:"",importFailures:[{directory:"bad-template",error:"invalid: stages[2].completion.check: line 44: unknown check"}]}});
      if(path==="/api/admin/meeting") {meeting={...meeting,name:body.name,template:body.template};meetingActive=true;return route.fulfill({json:meeting});}
      if(path==="/api/admin/exercises" || path==="/api/admin/checks/run") return route.fulfill({json:{ok:true}});
      throw new Error(`Unexpected fixture request ${req.method()} ${path}`);
    });
    const attempts=()=>writes.filter(w=>w.path.endsWith("/attempt"));
    const nav=()=>page.getByRole("navigation",{name:"Library views"});

    // Template-first library: one card per template, metadata, availability and filters.
    await page.goto(app+"/labs/");
    await page.getByRole("heading",{name:"Practice",exact:true}).waitFor();
    await page.getByRole("link",{name:"Explore Branch services revision 1234567"}).waitFor();
    assert.equal(await nav().getByRole("link",{name:"Library"}).getAttribute("aria-current"),"page","templates are the default view");
    assert.equal(await page.getByRole("link",{name:/Explore /}).count(),4,"one card per template, not per revision; coach-only hidden from students");
    assert.equal(await page.getByRole("link",{name:"Published labs"}).count(),0,"no published-lab navigation");
    const bank=page.getByRole("listitem").filter({has:page.getByRole("link",{name:"Explore Branch services revision 1234567"})});
    for(const text of ["Incident response","Intermediate","1 h 30 min","2 machines · 4 stages · 1 scenario","Workspace ready"]) await bank.getByText(text,{exact:true}).waitFor();
    for(const text of ["ccdc","Baseline practice","1 scenario"]) assert.equal(await bank.getByText(text,{exact:true}).count(),0,"cards omit tags and separate practice metadata");
    await page.getByRole("listitem").filter({has:page.getByRole("link",{name:/Explore Network investigation/})}).getByText("Preview only",{exact:true}).waitFor();
    await page.getByRole("listitem").filter({has:page.getByRole("link",{name:/Explore DNS recovery/})}).getByText("Local only",{exact:true}).waitFor();
    await page.getByRole("listitem").filter({has:page.getByRole("link",{name:/Explore Ubuntu basics/})}).getByText("Local only",{exact:true}).waitFor();
    assert(await fits(page),"library width");
    await shot(page,"library",width);
    await page.getByLabel("Search templates").fill("nmap");
    await page.getByText("1 of 4 templates shown").waitFor();
    await page.getByLabel("Search templates").fill("sweep");
    await page.getByText("1 of 4 templates shown").waitFor();
    await page.getByRole("link",{name:/Explore Ubuntu basics/}).waitFor();
    await page.getByLabel("Search templates").fill("");
    await page.getByLabel("Category").selectOption("Networking");
    await page.getByText("1 of 4 templates shown").waitFor();
    await page.getByLabel("Practice mode").selectOption("scenarios");
    await page.getByRole("heading",{name:"No matching templates"}).waitFor();
    if(width<600) await shot(page,"library-filtered",width);
    await page.getByRole("button",{name:"Clear filters"}).click();
    await page.getByRole("link",{name:/Explore /}).nth(2).waitFor();
    assert.equal(await page.getByLabel("Category").inputValue(),"");
    await page.getByLabel("Practice mode").selectOption("scenarios");
    await page.getByText("3 of 4 templates shown").waitFor();
    await page.getByLabel("Difficulty").selectOption("beginner");
    await page.getByText("1 of 4 templates shown").waitFor();
    await page.getByRole("link",{name:/Explore Ubuntu basics/}).waitFor();
    await page.goto(app+"/admin/labs/");
    await page.getByRole("heading",{name:"For meeting administrators"}).waitFor();
    assert.equal(new URL(page.url()).pathname,"/admin/labs/","students are not redirected into administration");

    // Exact revision detail: baseline and scenario starts, attempts filtered by pin.
    await page.goto(app+"/labs/?q=bank");
    await page.getByRole("link",{name:"Explore Branch services revision 1234567"}).click();
    await page.getByRole("heading",{name:"Practice",exact:true}).waitFor();
    await page.getByText("ccdc",{exact:true}).waitFor();
    await page.getByText("web",{exact:true}).waitFor();
    const yours=page.getByRole("region",{name:"Your attempts on this revision"});
    await yours.getByText("Branch outage",{exact:true}).waitFor();
    assert.equal(await yours.getByRole("listitem").count(),2,"attempt history matches the exact revision");
    assert.equal(await page.getByRole("link",{name:"Branch discovery",exact:true}).count(),0);
    await page.getByText("2–3 issues · 6 variants",{exact:true}).waitFor();
    assert.equal(await page.getByText("Change Apache port").count(),0,"scenario summaries contain no break choices");
    await page.getByLabel("Stage",{exact:true}).selectOption("restore");
    await page.getByRole("heading",{name:"Restore the listener"}).waitFor();
    await page.getByText("Checked automatically",{exact:true}).waitFor();
    await page.clock.runFor(10500);
    assert.equal(await page.getByLabel("Stage",{exact:true}).inputValue(),"restore","same-session refresh preserves selected template stage");
    assert.equal(await page.getByText("Set the listener",{exact:false}).count(),0,"public preview omits solution");
    await page.getByLabel("Stage",{exact:true}).selectOption("review");
    await page.getByText("Instructor",{exact:true}).waitFor();
    await page.getByLabel("Stage",{exact:true}).selectOption("restore");
    assert.equal(await page.locator("article script").count(),0,"raw HTML disabled");
    assert(await fits(page),"detail width");
    await shot(page,"template-detail",width);
    await page.getByLabel("Revision",{exact:true}).selectOption("older");
    await page.waitForFunction(()=>document.getElementById("template-revision")?.value==="older");
    await yours.getByRole("listitem").first().waitFor();
    assert.equal(await yours.getByRole("listitem").count(),1,"switching revision keeps the exact pin");
    assert.equal(await page.getByRole("button",{name:/New scenario/}).count(),0,"older revision has no scenarios");
    await page.goBack();
    await page.waitForFunction(()=>document.getElementById("template-revision")?.value==="latest");
    await page.getByRole("link",{name:"All templates"}).click();
    assert.equal(await page.getByLabel("Search templates").inputValue(),"bank","search persists on return");
    await page.goBack();

    // Unknown result: the retry repeats the same request ID; nothing rerolls automatically.
    plan.push("abort");
    await page.getByRole("button",{name:"Start practice: Branch services"}).click();
    await page.getByText("couldn't confirm whether your attempt was created",{exact:false}).waitFor();
    if(width<600) await shot(page,"attempt-unknown",width);
    await page.clock.runFor(31000);
    assert.equal(attempts().length,1,"pending mutation is not retried automatically");
    await page.getByRole("button",{name:"Retry: Start practice: Branch services"}).click();
    await page.waitForURL("**/learn/?id=attempt-1");
    assert.equal(attempts()[1].body.requestID,attempts()[0].body.requestID,"unknown-result retry reuses its request ID");
    assert.deepEqual(attempts()[1].body,{requestID:attempts()[0].body.requestID});
    await page.getByRole("heading",{name:"Branch services · Baseline practice",level:1}).waitFor();
    await page.getByText("Baseline practice attempt",{exact:true}).waitFor();
    assert.equal(await page.getByRole("link",{name:"Template revision"}).getAttribute("href"),"/labs/?template=latest");
    await page.getByRole("button",{name:"Run locally"}).waitFor();

    // A committed attempt whose response was lost replays (200) on retry instead of duplicating.
    await page.goto(app+"/labs/?template=latest");
    const before=labs.length; plan.push("lost");
    await page.getByRole("button",{name:"New scenario: Branch outage"}).click();
    await page.getByText("couldn't confirm",{exact:false}).waitFor();
    await page.getByRole("button",{name:"Retry: New scenario: Branch outage"}).click();
    await page.waitForURL("**/learn/?id=attempt-2");
    assert.equal(labs.length,before+1,"replayed request created one attempt");
    assert.deepEqual(attempts().at(-1).body,{requestID:attempts().at(-2).body.requestID,scenarioID:"outage"});
    assert.notEqual(attempts().at(-1).body.requestID,attempts()[0].body.requestID,"each mode has its own request");
    const briefing=page.getByRole("region",{name:"Branch outage"});
    await briefing.getByText("Randomized scenario",{exact:true}).waitFor();
    await briefing.getByText("2–3 issues",{exact:true}).waitFor();
    assert.equal(await page.getByRole("button",{name:"Run locally"}).count(),0,"scenarios are cloud-only");
    await page.getByText("Scenarios run only in a private workspace.",{exact:false}).waitFor();
    for(const hidden of ["Change Apache port","Stop the web service","port","variant 3"]) assert.equal(await page.getByText(hidden,{exact:true}).count(),0,"selected breaks stay hidden");
    assert(await fits(page),"scenario player width");
    await shot(page,"learn-scenario",width);

    // Completion offers a new randomized attempt on the exact pin.
    labs.find(l=>l.id==="attempt-2").completedSteps=2;
    await page.clock.runFor(20500);
    await page.getByText("Lab complete.",{exact:false}).waitFor();
    await shot(page,"learn-complete",width);
    await page.getByRole("button",{name:"New scenario: Branch outage"}).click();
    await page.waitForURL("**/learn/?id=attempt-3");
    assert.equal(attempts().at(-1).path,"/api/templates/latest/attempt");
    assert.equal(attempts().at(-1).body.scenarioID,"outage");
    assert.notEqual(attempts().at(-1).body.requestID,attempts().at(-2).body.requestID,"a confirmed attempt never reuses its request ID");
    labs.find(l=>l.id==="a-base").completedSteps=2;
    await page.goto(app+"/learn/?id=a-base");
    await page.getByRole("button",{name:"Try again: Branch services · Baseline practice"}).click();
    await page.waitForURL("**/learn/?id=attempt-4");
    assert.deepEqual(Object.keys(attempts().at(-1).body),["requestID"]);

    // A confirmed rejection is reported and the next explicit start is a new request.
    await page.goto(app+"/labs/?template=latest");
    plan.push("reject");
    await page.getByRole("button",{name:"Start practice: Branch services"}).click();
    await page.getByText("Stop your active cloud workspace before starting another scenario.",{exact:true}).waitFor();
    await page.getByRole("button",{name:"Start practice: Branch services"}).click();
    await page.waitForURL("**/learn/?id=attempt-5");
    assert.notEqual(attempts().at(-1).body.requestID,attempts().at(-2).body.requestID,"rejected requests are not replayed");

    // History is resumable independently; legacy publications stay separate.
    await page.goto(app+"/labs/?view=attempts");
    assert.equal(await page.getByRole("heading",{name:"Earlier published labs"}).count(),0);
    const history=page.getByRole("region",{name:"My attempts"});
    await history.getByRole("listitem").first().waitFor();
    assert.equal(await history.getByRole("listitem").count(),8);
    assert.equal(await history.getByRole("link",{name:"Branch discovery"}).count(),0,"legacy lessons are not listed as attempts");
    assert.equal(await nav().getByRole("link",{name:/My attempts/}).getAttribute("aria-current"),"page");
    const olderCard=history.getByRole("listitem").filter({has:page.getByRole("link",{name:/^Continue Branch services · Baseline practice/})}).last();
    assert.equal(await olderCard.getByRole("link",{name:/Template revision/}).getAttribute("href"),"/labs/?template=older");
    assert(await fits(page),"history width");
    await shot(page,"attempts",width);

    // Unavailable modes explain themselves; local baseline remains available when unbuilt.
    await page.goto(app+"/labs/?template=network");
    await page.getByText("This revision has no self-paced stages.",{exact:true}).waitFor();
    assert(await page.getByRole("button",{name:"Start practice: Network investigation"}).isDisabled());
    await page.goto(app+"/labs/?template=ubuntu");
    await page.getByText("Local only · images not built",{exact:true}).waitFor();
    assert(await page.getByRole("button",{name:"Start practice: Ubuntu basics"}).isEnabled());
    assert(await page.getByRole("button",{name:"New scenario: Service sweep"}).isDisabled());
    await page.getByText("Unavailable · images not built",{exact:false}).waitFor();
    await shot(page,"template-unbuilt",width);
    await page.goto(app+"/labs/?template=dns");
    await page.getByText("Local only · no desktop",{exact:true}).waitFor();
    assert(await page.getByRole("button",{name:"Start practice: DNS recovery"}).isEnabled(),"baseline stays available for local runs");
    assert(await page.getByRole("button",{name:"New scenario: Resolver failure"}).isDisabled(),"built images alone do not make scenarios ready");
    await page.getByText("Unavailable · no desktop",{exact:true}).waitFor();

    // A lost response survives a hard reload: the retry repeats the stored request ID
    // and routes to the attempt that the server already created.
    await page.goto(app+"/labs/?template=latest");
    const created=labs.length; plan.push("lost");
    await page.getByRole("button",{name:"Start practice: Branch services"}).click();
    await page.getByText("couldn't confirm",{exact:false}).waitFor();
    const lostID=attempts().at(-1).body.requestID, lostLab=labs.at(-1).id;
    const stored=await page.evaluate(()=>sessionStorage.getItem("labcentral.attemptRequests"));
    assert.deepEqual(JSON.parse(stored),{owner:"student",requests:{'["latest",""]':lostID}},"only identity, pin, mode and request ID are stored");
    assert(!/csrf|instructions|hostname/.test(stored),"no credentials or lesson content stored");
    await page.reload();
    await page.getByRole("button",{name:"Retry: Start practice: Branch services"}).waitFor();
    await page.getByText("An earlier start wasn't confirmed.",{exact:false}).waitFor();
    if(evidence){await page.getByRole("button",{name:"Retry: Start practice: Branch services"}).evaluate(e=>e.scrollIntoView({block:"center"}));await page.screenshot({path:`${evidence}/attempt-reload-${width}.png`});}
    assert.equal(attempts().at(-1).body.requestID,lostID,"reload does not resend automatically");
    await page.getByRole("button",{name:"Retry: Start practice: Branch services"}).click();
    await page.waitForURL(`**/learn/?id=${lostLab}`);
    assert.equal(attempts().at(-1).body.requestID,lostID,"retry after reload repeats the same request ID");
    assert.equal(labs.length,created+1,"replay after reload created no duplicate");
    assert.equal(await page.evaluate(()=>sessionStorage.getItem("labcentral.attemptRequests")),null,"confirmed outcome clears the stored request");

    // Sign-out discards unconfirmed request IDs; the next start is a new request.
    await page.goto(app+"/labs/?template=latest");
    plan.push("abort");
    await page.getByRole("button",{name:"New scenario: Branch outage"}).click();
    await page.getByText("couldn't confirm",{exact:false}).waitFor();
    signedIn=false; await page.clock.runFor(10500);
    await page.getByRole("heading",{name:"Sign in to continue"}).waitFor();
    assert.equal(await page.evaluate(()=>sessionStorage.getItem("labcentral.attemptRequests")),null,"sign-out clears pending request IDs");
    signedIn=true; await page.reload();
    await page.getByRole("button",{name:"New scenario: Branch outage"}).waitFor();
    assert.equal(await page.getByRole("button",{name:/^Retry: /}).count(),0);

    // Confirmed logout through the UI discards an unconfirmed request; a later
    // sign-in by the same user starts a new request instead of resurrecting it.
    await page.goto(app+"/labs/?template=latest");
    plan.push("lost");
    await page.getByRole("button",{name:"Start practice: Branch services"}).click();
    await page.getByText("couldn't confirm",{exact:false}).waitFor();
    const loggedOutID=attempts().at(-1).body.requestID;
    assert.notEqual(await page.evaluate(()=>sessionStorage.getItem("labcentral.attemptRequests")),null);
    // The next session read fails transiently, so only the explicit logout clear applies.
    meFailure=true;
    await page.getByRole("button",{name:"Sign out"}).click();
    await page.getByRole("heading",{name:"Hands-on labs for team practice."}).waitFor();
    assert.equal(logouts,1);
    assert.equal(await page.evaluate(()=>sessionStorage.getItem("labcentral.attemptRequests")),null,"confirmed logout clears pending request IDs");
    meFailure=false; signedIn=true;
    await page.goto(app+"/labs/?template=latest");
    await page.getByRole("button",{name:"Start practice: Branch services"}).waitFor();
    assert.equal(await page.getByRole("button",{name:/^Retry: /}).count(),0,"same-user sign-in does not resurrect the old request");
    await page.getByRole("button",{name:"Start practice: Branch services"}).click();
    await page.waitForURL(/learn\/\?id=attempt-/);
    assert.notEqual(attempts().at(-1).body.requestID,loggedOutID,"a new sign-in uses a new request ID");

    // A late attempt response cannot navigate after logout is confirmed, even while
    // the hard navigation to the signed-out home page is still pending.
    await page.goto(app+"/labs/?template=latest");
    let releaseLate; heldAttempt=new Promise(r=>releaseLate=r); plan.push("hold");
    const lateSent=attempts().length;
    await page.getByRole("button",{name:"Start practice: Branch services"}).click();
    while(attempts().length===lateSent) await new Promise(r=>setTimeout(r,20));
    let releaseHome, homeRequested; const homeHeld=new Promise(r=>releaseHome=r), homeSeen=new Promise(r=>homeRequested=r);
    await page.route(app+"/",async route=>{homeRequested();await homeHeld;await route.continue();},{times:1});
    const visited=[], learnReads=[];
    const onNav=f=>{if(f===page.mainFrame())visited.push(f.url());};
    const onRequest=r=>{if(/\/api\/learning\/attempt-/.test(r.url()))learnReads.push(r.url());};
    page.on("framenavigated",onNav); page.on("request",onRequest);
    // The click resolves only after its held navigation is released below.
    const signingOut=page.getByRole("button",{name:"Sign out"}).click();
    await homeSeen;
    const lateResponse=page.waitForResponse(r=>r.url().endsWith("/api/templates/latest/attempt"));
    releaseLate(); await lateResponse;
    await new Promise(r=>setTimeout(r,300));
    assert.deepEqual(visited.filter(u=>u.includes("/learn/")),[],"late response after logout does not navigate");
    assert.deepEqual(learnReads,[],"late response after logout loads no attempt");
    releaseHome(); await signingOut;
    await page.getByRole("heading",{name:"Hands-on labs for team practice."}).waitFor();
    page.off("framenavigated",onNav); page.off("request",onRequest);
    assert.equal(logouts,2);
    assert.equal(await page.evaluate(()=>sessionStorage.getItem("labcentral.attemptRequests")),null);
    signedIn=true;
    await page.goto(app+"/labs/");
    await page.getByRole("heading",{name:"Practice",exact:true}).waitFor();

    // A transient session failure keeps stored IDs; a confirmed unauthenticated load clears them.
    const seeded=JSON.stringify({owner:"student",requests:{'["latest",""]':"0123456789abcdef01234567"}});
    await page.evaluate(v=>sessionStorage.setItem("labcentral.attemptRequests",v),seeded);
    meFailure=true; await page.reload();
    await page.getByText("Your lab information could not be loaded.",{exact:false}).waitFor();
    assert.equal(await page.evaluate(()=>sessionStorage.getItem("labcentral.attemptRequests")),seeded,"transient failure keeps pending IDs");
    meFailure=false; signedIn=false; await page.reload();
    await page.getByRole("heading",{name:"Sign in to continue"}).waitFor();
    assert.equal(await page.evaluate(()=>sessionStorage.getItem("labcentral.attemptRequests")),null,"confirmed unauthenticated load clears stale IDs");
    signedIn=true;

    // One live cloud run blocks new attempts, as the server does.
    const sentBeforeRun=attempts().length;
    activeRun={id:"run",labID:"a-base",userID:"student",status:"ready",created:iso(0),expires:iso(3600000)};
    await page.goto(app+"/labs/?template=latest");
    await page.getByRole("link",{name:"Open lab"}).waitFor();
    assert(await page.getByRole("button",{name:"Start practice: Branch services"}).isDisabled());
    assert(await page.getByRole("button",{name:"New scenario: Branch outage"}).isDisabled());
    assert.equal(await page.getByText("Stop your workspace and wait for cleanup before starting another attempt.",{exact:true}).count(),2);
    if(width>600) await shot(page,"template-run-active",width);
    await page.goto(app+"/learn/?id=a-base");
    await page.getByText("Lab complete.",{exact:false}).waitFor();
    assert(await page.getByRole("button",{name:"Try again: Branch services · Baseline practice"}).isDisabled(),"repeat waits for cleanup");
    activeRun=null;
    assert.equal(attempts().length,sentBeforeRun,"blocked controls sent nothing");

    // Unavailable sessionStorage falls back to in-memory request IDs.
    const restricted=await context.newPage(), restrictedErrors=[];
    restricted.on("pageerror",e=>restrictedErrors.push(e.message));
    await restricted.addInitScript(()=>Object.defineProperty(window,"sessionStorage",{configurable:true,get(){throw new DOMException("Storage disabled","SecurityError")}}));
    await restricted.goto(app+"/labs/?template=ubuntu");
    plan.push("abort");
    await restricted.getByRole("button",{name:"Start practice: Ubuntu basics"}).click();
    await restricted.getByText("couldn't confirm",{exact:false}).waitFor();
    const memoryID=attempts().at(-1).body.requestID;
    await restricted.getByRole("button",{name:"Retry: Start practice: Ubuntu basics"}).click();
    await restricted.waitForURL(/learn\/\?id=attempt-/);
    assert.equal(attempts().at(-1).body.requestID,memoryID,"retry works without sessionStorage");
    assert.deepEqual(restrictedErrors,[]);
    await restricted.close();

    // A late attempt response cannot navigate or report into another identity.
    await page.goto(app+"/labs/?template=ubuntu");
    let releaseAttempt; heldAttempt=new Promise(r=>releaseAttempt=r); plan.push("hold");
    await page.getByRole("button",{name:"Start practice: Ubuntu basics"}).click();
    const studentRequest=attempts().at(-1).body.requestID;
    const identityRead=page.waitForResponse(r=>r.url().endsWith("/api/me"));
    user="other"; await page.clock.runFor(10500);
    await (await identityRead).finished(); await page.clock.runFor(100);
    releaseAttempt(); await page.clock.runFor(500);
    await page.getByRole("button",{name:"Start practice: Ubuntu basics"}).waitFor();
    assert.equal(new URL(page.url()).search,"?template=ubuntu","late response for the previous identity does not navigate");
    assert.equal(await page.getByRole("alert").count(),0);
    await page.getByRole("button",{name:"Start practice: Ubuntu basics"}).click();
    await page.waitForURL(/learn\/\?id=attempt-/);
    assert.equal(attempts().at(-1).user,"other");
    assert.notEqual(attempts().at(-1).body.requestID,studentRequest,"request IDs are scoped to identity");
    user="student"; await page.clock.runFor(10500);

    // Legacy lesson links keep working, including solution isolation.
    practiceFailure=true;
    await page.goto(app+"/labs/?template=latest");
    await page.getByText("Your attempts could not be loaded",{exact:false}).waitFor();
    assert.equal(await page.getByText("No attempts on this revision yet.").count(),0,"failed learning load is not empty history");
    practiceFailure=false;await page.getByRole("button",{name:"Retry attempts"}).click();
    await page.getByRole("region",{name:"Your attempts on this revision"}).getByRole("listitem").first().waitFor();
    await page.goto(app+"/learn/?id=practice");
    await page.getByRole("button",{name:"View solution"}).click();
    await page.getByText("The branch has",{exact:false}).waitFor();
    await page.clock.runFor(21000);
    await page.getByText("The branch has",{exact:false}).waitFor();
    assert.equal(await page.getByText("1 of 2 stages complete",{exact:false}).count(),0,"revealing solution does not advance progress");
    await page.getByRole("button",{name:"Next stage",exact:true}).click();
    assert.equal(await page.getByText("The branch has",{exact:false}).count(),0,"solution cleared when stage changes");
    await page.getByRole("button",{name:"Previous stage",exact:true}).click();
    let releaseSolution;heldSolution=new Promise(r=>releaseSolution=r);
    await page.getByRole("button",{name:"View solution"}).click();
    await page.getByRole("button",{name:"Next stage",exact:true}).click();
    releaseSolution();
    await page.clock.runFor(500);
    assert.equal(await page.getByText("The branch has",{exact:false}).count(),0,"late solution cannot populate another stage");
    await page.getByRole("button",{name:"Previous stage",exact:true}).click();
    let releaseIdentitySolution; heldSolution=new Promise(r=>releaseIdentitySolution=r);
    await page.getByRole("button",{name:"View solution"}).click();
    const solutionIdentity=page.waitForResponse(r=>r.url().endsWith("/api/me"));
    user="other"; await page.clock.runFor(10500);
    await (await solutionIdentity).finished(); await page.clock.runFor(100);
    releaseIdentitySolution(); await page.clock.runFor(500);
    assert.equal(await page.getByText("The branch has",{exact:false}).count(),0,"late solution cannot populate another identity");

    // Administrators manage images; publishing navigation is gone and its route redirects.
    user="student"; admin=true; meetingActive=false;
    emptyCatalog=true;
    await page.goto(app+"/admin/templates/");
    await page.getByRole("heading",{name:"No templates yet",exact:true}).waitFor();
    assert.equal(await page.getByText("Loading templates…",{exact:true}).count(),0,"an empty installation finishes loading");
    emptyCatalog=false;
    await page.goto(app+"/labs/?template=coach");
    await page.getByText("Coach-only",{exact:true}).waitFor();
    // Coaches may practice coach-only revisions privately; students never list them.
    assert(!await page.getByRole("button",{name:"Start practice: Coach support"}).isDisabled());
    assert(!await page.getByRole("button",{name:"New scenario: Coach drill"}).isDisabled());
    assert.equal(await page.getByText("Coach-only revisions are previews for instructors.",{exact:true}).count(),0);
    await page.goto(app+"/labs/?template=network");
    const administration=page.getByRole("region",{name:"Administration",exact:true});
    await administration.getByRole("button",{name:"Build images",exact:true}).waitFor();
    assert.equal(await page.getByRole("link",{name:"Publish practice"}).count(),0,"no publishing call to action");
    assert.equal(await page.getByRole("link",{name:"Prepare meeting",exact:true}).count(),0,"unbuilt revision guides image administration");
    await page.goto(app+"/admin/labs/?template=latest");
    await page.waitForURL(app+"/admin/templates/");
    const adminNav=page.getByRole("navigation",{name:"Administration"});
    await adminNav.getByRole("link",{name:"Templates"}).waitFor();
    assert.deepEqual(await adminNav.getByRole("link").allInnerTexts(),["Meeting","Templates"]);
    const cards=page.getByRole("region",{name:"Templates",exact:true});
    const branch=cards.getByRole("listitem").filter({has:page.getByRole("link",{name:"Explore Branch services revision 1234567"})});
    assert.equal(await cards.getByRole("link",{name:/Explore /}).count(),5,"admin cards include coach templates, with one card per name");
    assert.equal(await branch.getByRole("link",{name:/Explore /}).getAttribute("href"),"/labs/?template=latest");
    await branch.getByText("Ready",{exact:true}).waitFor();
    assert(await fits(page),"admin templates width");
    await shot(page,"admin-templates",width);
    const creations=()=>writes.filter(w=>w.path==="/api/admin/meeting");
    assert.equal(creations().length,0,"exploring a revision does not provision");
    await page.goto(app+"/admin/");
    await page.getByRole("heading",{name:"No meeting running",exact:true}).waitFor();
    assert.equal(await page.getByRole("button",{name:"Create meeting",exact:true}).count(),0);
    await page.getByRole("link",{name:"Choose a template",exact:true}).click();
    await page.waitForURL(app+"/admin/templates/");

    // A card opens a dialog pinned to its newest revision, with validated settings.
    await page.getByRole("button",{name:"Start meeting: Branch services",exact:true}).click();
    const dialog=page.getByRole("dialog",{name:"Start a meeting",exact:true});
    await dialog.waitFor();
    assert((await dialog.innerText()).includes("1234567"));
    assert.equal(await dialog.getByLabel("Name",{exact:true}).inputValue(),"Branch services");
    assert.equal(await dialog.getByLabel("Number of teams").inputValue(),"5");
    assert.equal(await dialog.getByLabel("Students per team").inputValue(),"6");
    assert.equal(await dialog.getByLabel("Duration (hours)").inputValue(),"4");
    await dialog.getByText("30 desktop places · deleted after 4 hours",{exact:true}).waitFor();
    const submit=dialog.getByRole("button",{name:"Create meeting",exact:true});
    for(const [label,valid,invalids] of [["Number of teams","5",["0","101","1.5"]],["Students per team","6",["0","101","1.5"]],["Duration (hours)","4",["0","9","1.5"]]]) {
      for(const value of invalids) {
        await dialog.getByLabel(label).fill(value);
        await submit.click();
        assert.equal(await dialog.locator("form.modal-box").evaluate(f=>f.checkValidity()),false);
        assert.equal(creations().length,0,"invalid settings do not submit");
      }
      await dialog.getByLabel(label).fill(valid);
    }
    assert(await fits(page),"meeting dialog width");
    await shot(page,"meeting-dialog",width);
    await dialog.getByRole("button",{name:"Cancel",exact:true}).click();
    await dialog.waitFor({state:"hidden"});
    assert.equal(creations().length,0,"cancel does not create a meeting");
    await page.getByRole("button",{name:"Start meeting: Branch services",exact:true}).click();
    await dialog.getByLabel("Name",{exact:true}).fill("Card meeting");
    await submit.click();
    await page.waitForURL(app+"/admin/");
    await page.getByRole("heading",{name:"Card meeting",exact:true}).waitFor();
    assert.deepEqual(creations().at(-1).body,{name:"Card meeting",template:"latest",hours:4,teamCount:5,capacity:6});
    await page.getByRole("navigation",{name:"Administration"}).getByRole("link",{name:"Templates",exact:true}).click();
    await branch.getByText("In meeting",{exact:true}).waitFor();
    await cards.getByRole("link",{name:"Open meeting",exact:true}).waitFor();
    assert.equal(await cards.getByRole("button",{name:/Start meeting/}).count(),0,"an open meeting replaces creation actions");
    await branch.getByRole("link",{name:/Explore /}).click();
    await administration.getByRole("link",{name:"Open meeting",exact:true}).waitFor();
    assert.equal(await administration.getByRole("button",{name:"Start meeting",exact:true}).count(),0);
    await page.locator("summary").filter({hasText:"Instructor notes"}).click();
    const notes = page.locator("details").filter({has:page.locator(":scope > summary").filter({hasText:"Instructor notes"})});
    await notes.getByRole("heading",{name:"Prepare the exercise",exact:true}).waitFor();
    assert.equal(await notes.locator("ol > li").count(),2,"coach notes render ordered preparation steps");
    assert.equal(await notes.getByRole("heading",{name:"Inspect the listener",exact:true}).isVisible(),false,"exercise walkthroughs stay collapsed until needed");
    await notes.locator("summary").filter({hasText:"Change Apache port"}).click();
    await notes.getByRole("heading",{name:"Inspect the listener",exact:true}).waitFor();
    assert((await notes.locator("pre code").innerText()).includes("sudo journalctl -u ssh"),"commands render as selectable code");
    assert.equal(await page.evaluate(()=>window.unsafeNotes),undefined,"raw HTML is disabled for private Markdown too");
    assert(await fits(page),"expanded instructor notes fit narrow screens with local code scrolling");
    await shot(page,"instructor-notes",width);

    // A historical detail revision is never substituted if its images become unavailable.
    meetingActive=false;
    await page.goto(app+"/labs/?template=latest");
    await page.getByLabel("Revision",{exact:true}).selectOption("older");
    await page.waitForURL(app+"/labs/?template=older");
    const startMeeting=administration.getByRole("button",{name:"Start meeting",exact:true});
    for(const unavailable of ["unbuilt","removing"]) {
      olderBuilt=true; olderRemoving=false;
      await page.clock.runFor(10500);
      await startMeeting.click();
      await dialog.waitFor();
      assert((await dialog.innerText()).includes("abc1234"));
      const updated=page.waitForResponse(async r=>r.url().endsWith("/api/admin") && (await r.json()).templates.some(t=>t.id==="older" && (unavailable==="unbuilt"?!t.built:t.removing)));
      if(unavailable==="unbuilt") olderBuilt=false; else olderRemoving=true;
      await page.clock.runFor(10500);
      await updated;
      await page.clock.runFor(100);
      assert(await submit.isDisabled(),"the exact pin must remain built and outside cleanup");
      await dialog.locator("form.modal-box").evaluate(f=>f.dispatchEvent(new Event("submit",{bubbles:true,cancelable:true})));
      await dialog.getByRole("alert").getByText("This revision is no longer built. Choose another template.",{exact:true}).waitFor();
      assert.equal(creations().length,1,"unavailable pin cannot submit or substitute the newer built revision");
      await page.clock.runFor(2500);
      assert(await submit.isDisabled(),"subsequent polls keep the pin unavailable");
      await dialog.getByRole("button",{name:"Cancel",exact:true}).click();
    }
    olderBuilt=true; olderRemoving=false;
    await page.clock.runFor(10500);
    await startMeeting.click();
    await dialog.getByLabel("Name",{exact:true}).fill("Historical meeting");
    await submit.click();
    await page.waitForURL(app+"/admin/");
    await page.getByRole("heading",{name:"Historical meeting",exact:true}).waitFor();
    assert.deepEqual(creations().at(-1).body,{name:"Historical meeting",template:"older",hours:4,teamCount:5,capacity:6});
    assert.equal(creations().length,2);

    // Independent meeting-stage fixture uses the current revision.
    meeting={...meeting,template:"latest"};
    // Meeting stages remain freely selectable and explicit.
    await page.goto(app+"/admin/#exercises");
    await page.getByLabel("Template stage").selectOption("discover");
    await page.getByText("Instructor solution",{exact:true}).click();
    await page.getByText("Accepted answers: 4, four (any letter case)",{exact:true}).waitFor();
    if(evidence)await page.screenshot({path:`${evidence}/meeting-answer-stage-${width}.png`});
    await page.getByLabel("Template stage").selectOption("restore");
    await page.getByText("Instructor solution",{exact:true}).click();
    await page.getByText("Set the listener to",{exact:false}).waitFor();
    assert.equal(writes.filter(w=>w.path.endsWith("/exercises")).length,0,"stage selection does not mutate guests");
    await page.getByRole("button",{name:"Select ready teams"}).click();
    if(evidence)await page.screenshot({path:`${evidence}/meeting-stages-${width}.png`,fullPage:true});
    await page.getByRole("button",{name:"Apply break to 2 teams"}).click();
    await page.getByRole("dialog").getByRole("button",{name:"Run selected exercise"}).click();
    await page.getByRole("dialog").waitFor({state:"hidden"});
    assert.deepEqual(writes.at(-1),{path:"/api/admin/exercises",body:{action:"port",teamIDs:["t1","t2"]},user:"student"});
    await Promise.all([page.waitForResponse(r=>r.url().endsWith("/api/admin/checks/run")), page.getByRole("button",{name:"Check services",exact:true}).click()]);
    assert.equal(writes.at(-1).path,"/api/admin/checks/run");
    await page.getByLabel("Template stage").selectOption("discover");
    assert(await page.getByRole("button",{name:"Apply break to 2 teams"}).isDisabled(),"stage without break does not expose a pretend action");
    await page.getByRole("button",{name:"Reset exercise baseline",exact:true}).click();
    await page.getByRole("dialog").getByRole("button",{name:"Reset selected teams"}).click();
    await page.getByRole("dialog").waitFor({state:"hidden"});
    assert.deepEqual(writes.at(-1),{path:"/api/admin/exercises",body:{action:"reset",teamIDs:["t1","t2"]},user:"student"});
    await page.getByText("Other template breaks · 1",{exact:true}).click();
    await page.getByLabel("Other template break",{exact:true}).selectOption("stop");
    await page.getByRole("button",{name:"Apply break to 2 teams"}).click();
    await page.getByRole("dialog").getByRole("button",{name:"Run selected exercise"}).click();
    await page.getByRole("dialog").waitFor({state:"hidden"});
    assert.deepEqual(writes.at(-1),{path:"/api/admin/exercises",body:{action:"stop",teamIDs:["t1","t2"]},user:"student"},"unreferenced template breaks remain operable");
    assert(await fits(page),"meeting stage width");
    const stageOnlyRead = page.waitForResponse(async r => r.url().endsWith("/api/admin") && (await r.json()).breaks.length === 0);
    stageOnly=true;await page.goto(app+"/admin/#exercises");
    await stageOnlyRead;
    await page.getByRole("tab",{name:"Exercises",exact:true}).waitFor();
    await page.getByLabel("Template stage").waitFor();
    await page.getByLabel("Template stage").locator("option").nth(1).waitFor({state:"detached"});
    assert.equal(await page.getByLabel("Template stage").locator("option").count(),1);
    assert.equal(await page.getByLabel("Template stage").inputValue(),"discover","stage-only meetings remain reachable");
    assert.equal(await page.getByRole("button",{name:"Reset exercise baseline",exact:true}).count(),0,"no reset action without an exercise baseline");
    assert.equal(await page.getByRole("button",{name:/Apply break/}).count(),0);
    admin=false;await page.clock.runFor(2500);
    await page.getByRole("heading",{name:"For meeting administrators"}).waitFor();
    assert.equal(await page.getByText("Instructor solution",{exact:true}).count(),0,"role loss clears private stage UI");
    let releaseDetail;heldDetail=new Promise(r=>releaseDetail=r);
    await page.goto(app+"/labs/?template=latest");
    signedIn=false;await page.clock.runFor(10500);releaseDetail();
    await page.clock.runFor(500);
    assert.equal(await page.getByRole("heading",{name:"Branch services",exact:true}).count(),0,"late template response cannot populate signed-out view");
    assert.deepEqual(errors,[]);
    await context.close();
    console.log(`PASS template library (local mocks): template-first cards/search/filters, exact revision pins, baseline/scenario attempts with unknown-result retry/replay/rejection, lost-response reload replay, sign-out clearing, storage-unavailable fallback, active-run and workstation readiness blocks, stage usage labels, scenario briefing without break disclosure, repeat on exact pin, attempt history and legacy links, unavailable-mode explanations, stale identity responses, admin redirect/navigation, meeting pins/stages, solution isolation (${width})`);
  }
} finally {await browser.close();}
