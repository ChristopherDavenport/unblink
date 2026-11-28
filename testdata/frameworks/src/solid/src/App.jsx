import { createSignal, onMount } from 'solid-js';

export default function App() {
  const [hydrated, setHydrated] = createSignal(false);
  const [bells, setBells] = createSignal(0);
  onMount(() => setHydrated(true));
  return (
    <main>
      <article>
        <h1>Solid Belfry Register</h1>
        <p>A Solid page rendered to a string at build time and hydrated in place by fine-grained reactivity.</p>
        <p id="hydration">{hydrated() ? 'Solid page hydrated' : 'awaiting hydration'}</p>
        <button id="ring" onClick={() => setBells(bells() + 1)}>
          Ring the bell
        </button>
        <p id="bells">Bells rung: {bells()}</p>
      </article>
    </main>
  );
}
