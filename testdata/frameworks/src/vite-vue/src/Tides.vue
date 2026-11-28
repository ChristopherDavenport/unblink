<script setup>
import { onMounted, ref } from 'vue';

const props = defineProps({ port: String });
const rows = ref([]);
const status = ref('tide table pending');

onMounted(async () => {
  const res = await fetch('/tides.json');
  const data = await res.json();
  rows.value = data.tides;
  status.value = data.station;
});
</script>

<template>
  <article>
    <h1>Vue Router Deep Link: {{ props.port }}</h1>
    <p>This route was resolved from the address bar by vue-router in history mode, then filled from a JSON file fetched after mount.</p>
    <p id="status">{{ status }}</p>
    <ul>
      <li v-for="row in rows" :key="row.at">{{ row.at }} reaches {{ row.height }}</li>
    </ul>
  </article>
</template>
