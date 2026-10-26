import { render } from 'preact'
import { App } from './app'
import './styles/tokens.css'
import './styles/app.css'
import './styles/screens.css'
import './styles/read.css'
import './styles/bots.css'
import './styles/browser.css'

const root = document.getElementById('app')
if (root) render(<App />, root)
