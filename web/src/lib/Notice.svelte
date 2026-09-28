<script lang="ts">import {CircleAlert} from '@lucide/svelte';let {message}: {message:string}=$props();</script>
{#if message}<div role="alert" class="alert alert-soft alert-error my-5"><CircleAlert size={18} aria-hidden="true"/><span>{message}</span></div>{/if}
