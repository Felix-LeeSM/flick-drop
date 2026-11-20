import adapter from '@sveltejs/adapter-static';
import { sveltekit } from '@sveltejs/kit/vite';
import tailwindcss from '@tailwindcss/vite';
import { defineConfig } from 'vitest/config';

export default defineConfig({
	envPrefix: ['VITE_', 'PUBLIC_'],
	build: {
		// The CSP (web/nginx-templates/csp.conf.template) blocks data: fonts, so never inline them.
		assetsInlineLimit: (filePath) => (/\.woff2?$/.test(filePath) ? false : undefined)
	},
	plugins: [
		tailwindcss(),
		sveltekit({
			compilerOptions: {
				// Runes mode for the project, not libraries; removable in svelte 6.
				runes: ({ filename }) =>
					filename.split(/[/\\]/).includes('node_modules') ? undefined : true
			},
			adapter: adapter({ fallback: '200.html' })
		})
	],
	test: {
		expect: { requireAssertions: true },
		projects: [
			{
				extends: './vite.config.ts',
				test: {
					name: 'server',
					environment: 'node',
					include: ['src/**/*.{test,spec}.{js,ts}'],
					exclude: ['src/**/*.svelte.{test,spec}.{js,ts}']
				}
			}
		]
	}
});
