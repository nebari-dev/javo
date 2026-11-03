import { describe, expect, it } from 'vitest'
import { fileKind, languageOf } from './fileKind'

describe('fileKind', () => {
  it('shows pictures and PDFs in the browser and tries everything else as text', () => {
    expect(['a.PNG', 'b.jpg', 'c.jpeg', 'd.gif', 'e.webp', 'f.svg'].map(fileKind)).toEqual(Array(6).fill('image'))
    expect(fileKind('report.pdf')).toBe('pdf')
    expect(['app.py', 'README', 'data.parquet', 'x.ipynb'].map(fileKind)).toEqual(Array(4).fill('text'))
  })
})

describe('languageOf', () => {
  it('picks the language by extension, or by a few well-known names', () => {
    const cases: [string, string][] = [
      ['app.py', 'python'],
      ['README.md', 'markdown'],
      ['notes.markdown', 'markdown'],
      ['x.json', 'json'],
      ['x.ipynb', 'json'],
      ['ci.yml', 'yaml'],
      ['values.yaml', 'yaml'],
      ['a.js', 'javascript'],
      ['a.mjs', 'javascript'],
      ['a.jsx', 'jsx'],
      ['a.ts', 'typescript'],
      ['a.tsx', 'tsx'],
      ['index.html', 'html'],
      ['site.css', 'css'],
      ['q.sql', 'sql'],
      ['run.sh', 'shell'],
      ['.bashrc', 'shell'],
      ['data.csv', 'plain'],
      ['Makefile', 'plain'],
    ]
    expect(cases.map(([name]) => [name, languageOf(name)])).toEqual(cases)
  })
})
