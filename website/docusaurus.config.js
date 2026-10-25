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
  onBrokenMarkdownLinks: 'throw',
  presets: [
    [
      'classic',
      {
        docs: {
          path: '../docs',
          routeBasePath: '/',
          sidebarPath: require.resolve('./sidebars.js'),
          // No trailing slash: Docusaurus appends the file path relative to
          // the site root (../docs/...), and GitHub normalizes the "/../".
          editUrl: 'https://github.com/RocketPadPlatforms/cloud-native-mail-service/edit/main',
          // "main" is published as "Next (unreleased)"; a docs version is cut
          // per minor release (see docs/roadmap.md and the release workflow).
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
    metaDescription:
      'CNMS is a Kubernetes operator by Rocket Pad Platforms that runs and manages Postfix, Rspamd and Dovecot mail servers with production-grade defaults.',
    navbar: {
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
