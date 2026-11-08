import path from 'node:path'

// PATH for every test daemon and CLI call, with a stub `paseo` first. paseo's
// CLI talks to the real paseo on its fixed localhost port regardless of HOME,
// so without the stub a test daemon's bots start sessions in the operator's
// real paseo.
export function hermeticPath(): string {
  return [path.resolve(__dirname, 'nopaseo'), process.env.PATH ?? '/usr/local/bin:/usr/bin:/bin'].join(':')
}
