import starlight from '@astrojs/starlight';
import { nebari } from '@nebari/starlight';
import { defineConfig, passthroughImageService } from 'astro/config';

const section = (label, directory) => ({
  label,
  items: [{ autogenerate: { directory } }],
});

export default defineConfig({
  // The only images are SVGs, which need no processing (and no sharp).
  image: { service: passthroughImageService() },
  // Pages read files from the repository (the chart's values.yaml, the
  // OpenAPI document), one level above the site.
  vite: { server: { fs: { allow: ['..'] } } },
  integrations: [
    starlight({
      title: 'Javo',
      description:
        'Javo runs notebooks, web apps and scheduled jobs for data teams on Kubernetes, from one place.',
      favicon: '/brand/favicon.svg',
      editLink: { baseUrl: 'https://github.com/nebari-dev/javo/edit/main/site/' },
      // Last-updated dates come from git history; off so builds of a shallow
      // clone or a tarball match.
      lastUpdated: false,
      // Javo's own footer on the splash pages (the theme's names Nebari).
      components: { Footer: './src/components/Footer.astro' },
      customCss: ['./src/styles/custom.css'],
      plugins: [
        nebari({
          githubHref: 'https://github.com/nebari-dev/javo',
          logo: {
            light: '/brand/javo-logo.svg',
            dark: '/brand/javo-logo-white.svg',
            alt: 'Javo',
          },
          nav: [
            { label: 'Get started', href: '/getting-started/' },
            { label: 'Use', href: '/using/' },
            { label: 'Administer', href: '/admin/' },
            { label: 'Architecture', href: '/architecture/' },
            { label: 'Reference', href: '/reference/' },
            { label: 'Contribute', href: '/contributing/' },
          ],
        }),
      ],
      sidebar: [
        section('Get started', 'getting-started'),
        section('Use Javo', 'using'),
        section('Administer', 'admin'),
        section('Architecture', 'architecture'),
        section('Reference', 'reference'),
        section('Contribute', 'contributing'),
      ],
    }),
  ],
});
