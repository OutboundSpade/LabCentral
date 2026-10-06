<script lang="ts">
 import '../app.css';import {onMount} from 'svelte';import {page} from '$app/state';import {Monitor,Users,SlidersHorizontal,LogOut,ArrowUpRight,GitBranch} from '@lucide/svelte';import {session,loading,refresh,api} from '$lib/api';import Notice from '$lib/Notice.svelte';let {children}=$props();let error=$state('');
 onMount(()=>{void refresh();const t=setInterval(refresh,10000);return()=>clearInterval(t)});
 async function logout(){try{await api('/auth/logout',{});location.assign('/')}catch(e){error=(e as Error).message;await refresh()}}
 const destinations=$derived([{href:'/teams/',label:'Teams',name:'Teams',icon:Users},{href:'/lab/',label:'Your lab',name:'Your lab',icon:Monitor},...($session?.user.admin?[{href:'/admin/',label:'Meeting',name:'Manage meeting',icon:SlidersHorizontal},{href:'/admin/templates/',label:'Templates',name:'Templates',icon:GitBranch}]:[])]);
 function active(href:string){return href==='/admin/templates/'?page.url.pathname.startsWith(href):page.url.pathname===href}
</script>
<svelte:head><title>LabCentral</title><link rel="icon" type="image/svg+xml" href="/brand.svg?v=corner-cell"/><meta name="description" content="Your team, your workspace, and today's CDC competition lab."/></svelte:head>
<a href="#main" class="sr-only focus:not-sr-only focus:absolute focus:z-10 focus:bg-base-100 focus:p-4">Skip to content</a>
<div class="min-h-screen bg-base-100 text-base-content">
 <header class="border-b border-base-content/10 bg-base-200/40">
  <div class="navbar mx-auto min-h-14 max-w-7xl gap-6 px-4 sm:px-6 lg:px-8">
   <a href="/" aria-label="LabCentral home" class="flex shrink-0 items-center gap-2.5 text-base font-semibold tracking-tight"><img src="/brand.svg?v=corner-cell" width="26" height="26" alt="" class="object-contain"/><span>LabCentral</span></a>
   {#if $session}
    <nav aria-label="Main navigation" class="fixed inset-x-0 bottom-0 z-10 flex justify-around border-t border-base-content/10 bg-base-200 pb-[env(safe-area-inset-bottom)] lg:static lg:mr-auto lg:justify-start lg:gap-1 lg:border-0 lg:bg-transparent lg:pb-0">
     {#each destinations as destination}
      <a href={destination.href} aria-label={destination.name} aria-current={active(destination.href)?'page':undefined} class={["flex min-h-16 min-w-16 flex-col items-center justify-center gap-1 border-t-2 px-3 text-xs hover:text-base-content lg:min-h-14 lg:flex-row lg:gap-2 lg:border-t-0 lg:border-b-2 lg:text-sm",active(destination.href)?"border-primary text-primary":"border-transparent text-base-content/75"]}><destination.icon size={17}/>{destination.label}</a>
     {/each}
    </nav>
    <span class="ml-auto hidden max-w-44 truncate text-xs text-base-content/75 sm:inline lg:ml-0">{$session.user.name}</span><button onclick={logout} class="btn btn-sm btn-ghost ml-auto min-h-11 min-w-11 sm:ml-0" aria-label="Sign out" title="Sign out"><LogOut size={17}/></button>
   {:else if !$loading}<a class="btn btn-sm min-h-11 btn-ghost ml-auto" href="https://auth.example.org">CDC ID <ArrowUpRight size={16}/></a>{/if}
  </div>
 </header>
 <main id="main" class="mx-auto max-w-7xl px-4 pt-6 pb-28 sm:px-6 lg:px-8 lg:py-8"><Notice message={error}/>{@render children()}</main>
</div>
