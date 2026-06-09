/** @type {import('tailwindcss').Config} */
module.exports = {
  content: ['./src/**/*.{html,js}'],
  theme: {
    extend: {
      colors: {
        spruce: {
          50: '#f2f8f8',
          100: '#e1efef',
          200: '#c5dfdf',
          300: '#9fcfcf',
          600: '#0c5c5e',
          700: '#094C4F',
          800: '#073c3e',
          900: '#052c2e',
        },
        flowly: {
          bg: '#E8EBED',
          card: '#FFFFFF',
          border: '#D1D7DC',
          textMain: '#101828',
          textMuted: '#5C6470',
        },
      },
      fontFamily: {
        sans: ['Segoe UI', '-apple-system', 'BlinkMacSystemFont', 'Roboto', 'Helvetica', 'Arial', 'sans-serif'],
        mono: ['Cascadia Code', 'Consolas', 'JetBrains Mono', 'monospace'],
      },
    },
  },
  plugins: [],
};
