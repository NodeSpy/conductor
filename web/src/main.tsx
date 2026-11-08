import { render } from 'preact'
import { App } from './app'
import { watchAdvanced } from './lib/advanced'
import './styles/tokens.css'
import './styles/app.css'
import './styles/marks.css'
import './styles/components.css'
import './styles/interact.css'
import './styles/filter.css'
import './styles/daylight.css'
import './styles/screens.css'
import './styles/legacy.css'
import './styles/read.css'
import './styles/bots.css'
import './styles/wallet.css'
import './styles/builder.css'

const root = document.getElementById('app')
if (root) render(<App />, root)
watchAdvanced()
