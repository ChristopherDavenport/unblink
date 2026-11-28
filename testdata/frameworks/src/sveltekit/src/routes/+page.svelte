<script>
  import { onMount } from 'svelte';

  // Svelte 5 runes.
  let hydrated = $state(false);
  let knots = $state(0);
  let fathoms = $derived(knots * 2);

  onMount(() => {
    hydrated = true;
  });
</script>

<svelte:head>
  <title>SvelteKit Runes Fixture</title>
</svelte:head>

<main>
  <article>
    <h1>SvelteKit Rope Walk</h1>
    <p>A prerendered SvelteKit page built with the static adapter and hydrated by the Svelte 5 runtime.</p>
    <p id="hydration">{hydrated ? 'Svelte page hydrated' : 'awaiting hydration'}</p>
    <button id="tie" onclick={() => (knots += 1)}>Tie a knot</button>
    <p id="knots">Knots tied: {knots}, fathoms paid out: {fathoms}</p>
  </article>
</main>
