/** @type {import('tailwindcss').Config} */
export default {
  content: [
    "./index.html",
    "./src/**/*.{vue,js,ts,jsx,tsx}",
  ],
  theme: {
    extend: {
      colors: {
        'primary': 'var(--bg-primary)',
        'secondary': 'var(--bg-secondary)',
        'tertiary': 'var(--bg-tertiary)',
        'dark': {
          100: '#1e1e1e', // Keeping for legacy, but prefer using semantic names below
          200: '#2d2d2d',
          300: '#3d3d3d',
          400: '#4d4d4d',
          500: '#5d5d5d',
        },
        'accent': {
          DEFAULT: 'var(--accent)',
          hover: 'var(--accent-hover)',
          dark: '#00b368',
        },
        'text': {
          primary: 'var(--text-primary)',
          secondary: 'var(--text-secondary)',
        },
        'border': 'var(--border-color)',
      },
      fontFamily: {
        sans: ['Nunito', 'system-ui', 'sans-serif'],
      },
    },
  },
  plugins: [],
  corePlugins: {
    preflight: false,
  }
}

