import { Component, afterNextRender, signal } from '@angular/core';

// A standalone, signal-based component. afterNextRender only runs in the
// browser, so its text proves the prerendered markup was hydrated.
@Component({
  selector: 'app-root',
  template: `
    <main>
      <article>
        <h1>Angular Lighthouse Log</h1>
        <p>A prerendered Angular route, hydrated in place by the browser bundle.</p>
        <p id="hydration">{{ hydrated() ? 'Angular app hydrated' : 'awaiting hydration' }}</p>
        <button id="sweep" (click)="sweeps.set(sweeps() + 1)">Sweep the lamp</button>
        <p id="sweeps">Lamp sweeps: {{ sweeps() }}</p>
      </article>
    </main>
  `,
})
export class App {
  readonly hydrated = signal(false);
  readonly sweeps = signal(0);

  constructor() {
    afterNextRender(() => this.hydrated.set(true));
  }
}
