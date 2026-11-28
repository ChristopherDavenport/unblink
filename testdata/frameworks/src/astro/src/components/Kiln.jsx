import { useEffect, useState } from 'preact/hooks';

export default function Kiln({ label }) {
  const [state, setState] = useState('island awaiting hydration');
  const [firings, setFirings] = useState(0);
  useEffect(() => setState(`${label} island hydrated`), [label]);
  return (
    <section class="kiln">
      <p class="state">{state}</p>
      <button class="fire" onClick={() => setFirings((n) => n + 1)}>
        Fire {label}
      </button>
      <p class="firings">
        {label} firings: {firings}
      </p>
    </section>
  );
}
