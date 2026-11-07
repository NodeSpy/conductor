import { render } from 'preact'
import { App } from './app'
import './styles/tokens.css'
import './styles/app.css'
import './styles/marks.css'
import './styles/components.css'
import './styles/daylight.css'
import './styles/filter.css'
import './styles/screens.css'

const root = document.getElementById('app')
if (root) render(<App />, root)
