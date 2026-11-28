import { StrictMode, Suspense, lazy, useState } from 'react';
import { createRoot } from 'react-dom/client';

// Used: fetched when the app renders.
const Orchard = lazy(() => import('./Orchard.jsx'));
// Never rendered: a browser never fetches this chunk, and neither may unblink.
const Cellar = lazy(() => import('./Cellar.jsx'));

function App() {
  const [showCellar, setShowCellar] = useState(false);
  return (
    <article>
      <h1>Vite React Orchard Ledger</h1>
      <p>
        A production Vite build of a React 19 application rendered this article,
        with a lazily imported route chunk for the orchard table below.
      </p>
      <Suspense fallback={<p>orchard pending</p>}>
        <Orchard />
      </Suspense>
      <button id="open-cellar" onClick={() => setShowCellar(true)}>
        Open cellar
      </button>
      {showCellar && (
        <Suspense fallback={<p>cellar pending</p>}>
          <Cellar />
        </Suspense>
      )}
    </article>
  );
}

createRoot(document.getElementById('root')).render(
  <StrictMode>
    <App />
  </StrictMode>,
);
