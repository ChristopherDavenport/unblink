<script setup>
// Data resolved during prerender travels to the client in the Nuxt payload; the
// client reads it from there, and must render the same text to hydrate cleanly.
const { data } = await useAsyncData('census', async () => ({
  keeper: 'Payload Keeper Ottoline',
  bees: 61110,
}));

// Compact notation: the server renders "61.1K". A client that formats this as
// "61,110" mismatches the server markup and Vue reports a hydration error.
const compact = new Intl.NumberFormat('en-US', { notation: 'compact', maximumFractionDigits: 1 });
const hydrated = ref(false);
const visits = ref(0);
onMounted(() => {
  hydrated.value = true;
});
</script>

<template>
  <main>
    <article>
      <h1>Nuxt Apiary Census</h1>
      <p>A statically generated Nuxt page whose figures come from a payload fetched at build time.</p>
      <p id="keeper">Keeper of record: {{ data.keeper }}</p>
      <p id="bees">Colony strength {{ compact.format(data.bees) }} bees</p>
      <p id="hydration">{{ hydrated ? 'Nuxt page hydrated' : 'awaiting hydration' }}</p>
      <button id="visit" @click="visits++">Record visit</button>
      <p id="visits">Hive visits: {{ visits }}</p>
    </article>
  </main>
</template>
