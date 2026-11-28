import { generateHydrationScript, renderToString } from 'solid-js/web';
import App from './App.jsx';

export function render() {
  return { html: renderToString(() => <App />), script: generateHydrationScript() };
}
