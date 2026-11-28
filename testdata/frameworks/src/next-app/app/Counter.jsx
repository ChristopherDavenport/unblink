'use client';

import { useEffect, useState } from 'react';

// A client component: its effect and its click handler only run if the page
// actually hydrated.
export default function Counter() {
  const [hydrated, setHydrated] = useState(false);
  const [count, setCount] = useState(0);
  useEffect(() => setHydrated(true), []);
  return (
    <section>
      <p id="hydration">{hydrated ? 'Next client component hydrated' : 'awaiting hydration'}</p>
      <button id="tally" onClick={() => setCount((c) => c + 1)}>
        Tally
      </button>
      <p id="count">Tally count is {count}</p>
    </section>
  );
}
