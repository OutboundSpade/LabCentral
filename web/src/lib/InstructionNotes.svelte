<script lang="ts">
  import { renderNotes } from "../../../features/shared/notes.js";
  let { content }: { content: string } = $props();
  function notes(node: HTMLElement, value: string) {
    renderNotes(node, value);
    return { update: (next: string) => renderNotes(node, next) };
  }
</script>
<div use:notes={content}></div>
