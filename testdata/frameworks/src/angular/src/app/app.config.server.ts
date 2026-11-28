import { ApplicationConfig, mergeApplicationConfig } from '@angular/core';
import { RenderMode, provideServerRendering, withRoutes } from '@angular/ssr';
import { appConfig } from './app.config';

const serverConfig: ApplicationConfig = {
  providers: [provideServerRendering(withRoutes([{ path: '**', renderMode: RenderMode.Prerender }]))],
};

export const config = mergeApplicationConfig(appConfig, serverConfig);
