// An async server component inside a Suspense boundary: prerendering resolves
// it, and the result travels in the streamed markup and the Flight payload.
export default async function Slow() {
  await new Promise((resolve) => setTimeout(resolve, 20));
  return <p>Suspense boundary resolved with the granary figures.</p>;
}
