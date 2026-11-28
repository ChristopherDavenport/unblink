import Link from 'next/link';
import { Suspense } from 'react';
import Counter from './Counter';
import Slow from './Slow';

export default function Home() {
  return (
    <main>
      <article>
        <h1>Next App Router Granary</h1>
        <p>A static export of a Next.js App Router page, prerendered on the server and hydrated from its Flight payload.</p>
        <Suspense fallback={<p>granary pending</p>}>
          <Slow />
        </Suspense>
        <Counter />
        <p>
          <Link href="/ledger/">Open the ledger page</Link>
        </p>
      </article>
    </main>
  );
}
