const { themes: prismThemes } = require('prism-react-renderer');

const lightCodeTheme = prismThemes.github;
const darkCodeTheme = prismThemes.githubDark;

/** @type {import('@docusaurus/types').Config} */
const config = {
  title: 'Cloud Native Mail Service',
  tagline: 'A Kubernetes operator for cloud-native email',
  favicon: 'img/favicon.svg',
  organizationName: 'RocketPadPlatforms',
  projectName: 'cloud-native-mail-service',
  url: 'https://rocketpadplatforms.github.io',
  baseUrl: '/cloud-native-mail-service/',
  onBrokenLinks: 'throw',
  onBrokenAnchors: 'throw',
  markdown: {
    hooks: {
      onBrokenMarkdownLinks: 'throw',
    },
  },
  themes: ['@docusaurus/theme-mermaid'],
  presets: [
    [
      'classic',
      {
        docs: {
          path: '../docs',
          routeBasePath: '/',
          sidebarPath: require.resolve('./sidebars.js'),
          // Docusaurus appends the path of the page relative to the site root
          // (website/), which for ../docs resolves to docs/ on GitHub.
          editUrl: 'https://github.com/RocketPadPlatforms/cloud-native-mail-service/edit/main/website/',
          // "main" is published as "Next (unreleased)"; a docs version is cut
          // per minor release once release automation exists (Phase 10, see
          // docs/roadmap.md).
          versions: {
            current: {
              label: 'Next (unreleased)',
              banner: 'unreleased',
            },
          },
        },
        blog: false,
        theme: {
          customCss: require.resolve('./src/css/custom.css'),
        },
      },
    ],
  ],
  themeConfig: {
    metadata: [
      {
        name: 'description',
        content:
          'CNMS is a Kubernetes operator by Rocket Pad Platforms that runs and manages Postfix, Rspamd and Dovecot mail servers with production-grade defaults.',
      },
    ],
    navbar: {
      // Title rendered next to the logo in the top bar (Docusaurus 3.10:
      // navbar.title, not logo.label).
      title: 'Cloud Native Mail Service',
      logo: {
        src: 'img/favicon.svg',
        alt: 'Cloud Native Mail Service',
      },
      items: [
        {
          type: 'docsVersionDropdown',
          position: 'left',
        },
        {
          href: 'https://github.com/RocketPadPlatforms/cloud-native-mail-service',
          label: 'GitHub',
          position: 'right',
        },
      ],
    },
    prism: {
      theme: lightCodeTheme,
      darkTheme: darkCodeTheme,
    },
  },
};

module.exports = config;
