import Link from 'next/link';

export const metadata = { title: 'Next Ledger Page' };

export default function Ledger() {
  return (
    <main>
      <article>
        <h1>Next Ledger Second Page</h1>
        <p>The second prerendered route of the export, reached by a Link from the granary page.</p>
        <p>
          <Link href="/">Back to the granary</Link>
        </p>
      </article>
    </main>
  );
}
